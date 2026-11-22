package host

// Multi-threaded downloads for BGE-M3 weights: the client fetches the pinned
// files itself with parallel Range chunks and SHA256 verification.

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

const (
	modelRepo     = "Xenova/bge-m3"
	modelRevision = "4de13258303883538bd53b696b452bf8099f0858"
	tokenizerSHA  = "6710678b12670bc442b99edc952c4d996ae309a7020c1fa0096dd245c2faf790"
)

type modelFileSpec struct {
	rel    string
	sha256 string
	size   int64
}

var modelFileSpecs = map[string]modelFileSpec{
	"tokenizer": {rel: "tokenizer.json", sha256: tokenizerSHA, size: 17082821},
	"quantized": {rel: "onnx/model_quantized.onnx", sha256: "0826f8c1ab9edf1801db86c61919d4d108e8bfc0b809ec823ad366882ff0b77d", size: 569694530},
	"fp16":      {rel: "onnx/model_fp16.onnx", sha256: "4f1a646a3d4f39985589e9991a717044ede8278617fe55e3d246838bc05055e9", size: 1133992936},
	"int8":      {rel: "onnx/model_int8.onnx", sha256: "a206e10e995aa2a833924bcd725ba5dd6c3425cd34bac3cf2b5677cd2a1c51d6", size: 568456694},
	"uint8":     {rel: "onnx/model_uint8.onnx", sha256: "3222211565d0d5da2fe89e0c40856ed879e5c5dd3369fbbe685213469498e811", size: 568456765},
	"q4":        {rel: "onnx/model_q4.onnx", sha256: "4f6d9786712f8dbf00efbb08ffd819395ef05d860249e852b4efc16022f753a6", size: 1248237611},
	"q4f16":     {rel: "onnx/model_q4f16.onnx", sha256: "8305fdfa48b5702ecb4a6d84c1a52a4d1cb27d69880ff7f69d1a93d795864f25", size: 699932840},
}

const m3CPUContainerURL = "https://github.com/risense-ai/respire-cli/releases/download/model-bge-m3-ort-v1/model_quantized.cpu-v1.ort"

var m3CPUContainer = modelFileSpec{
	rel: "onnx/model_quantized.cpu-v1.ort", size: 569654128,
	sha256: "7ff152e912bc234f5c90e4012f99897a7b2ce180f016db2321304057756b64ff",
}

var modelMirrors = []string{"https://hf-mirror.com", "https://hf-mirror.net", "https://huggingface.co"}

var errCorruptModelCleanup = errors.New("无法清理校验失败的模型下载文件")

// ModelDir matches the CLI's m3_model_dir(): RSRS_DATA_DIR (already resolved
// into DataRoot) or ~/.rsrs, plus models/bge-m3.
func (m *Manager) ModelDir() string {
	return filepath.Join(m.DataRoot, "models", "bge-m3")
}

// modelDirFor resolves the weight directory for one catalog model: profile
// models live under models/<id>; m3 keeps the legacy resolution.
func (m *Manager) modelDirFor(model string) (string, modelDefinition, error) {
	definition, err := modelDefinitionByID(model)
	if err != nil {
		return "", definition, err
	}
	if definition.Legacy {
		return m.ModelDir(), definition, nil
	}
	return m.ProfileDir(model), definition, nil
}

// modelClient grants each segment group its own pool. It keeps the manager's
// proxy/redirect/TLS setup, but has no overall deadline of its own.
func (m *Manager) modelClient() *http.Client {
	client := &http.Client{}
	var transport *http.Transport
	if m.modelHTTPClient != nil {
		template := m.modelHTTPClient
		client.Jar = template.Jar
		client.CheckRedirect = template.CheckRedirect
		if template.Transport != nil {
			base, ok := template.Transport.(*http.Transport)
			if !ok {
				// Internal test transports are provided as-is, not owned here.
				client.Transport = template.Transport
				return client
			}
			transport = base.Clone()
		}
	}
	if transport == nil {
		transport = http.DefaultTransport.(*http.Transport).Clone()
		if m.proxy != nil {
			transport.Proxy = m.proxy.proxyFunc(func() string { return m.Preferences().ProxyMode })
		}
	}
	if transport.ResponseHeaderTimeout == 0 || transport.ResponseHeaderTimeout > modelDownloadStall {
		transport.ResponseHeaderTimeout = modelDownloadStall
	}
	// Preserve the supplied per-host limit (default: none). An abandoned
	// SOCKS dial can occupy a slot until group teardown. A new one-slot
	// cap would prevent retries from connecting to a recovered proxy.
	preserveModelProxyStatus(transport)
	client.Transport = transport
	return client
}

// ValidModelMirror accepts the application default or a single HTTP(S) mirror
// origin/base path. Custom mirrors remain supported; embedded credentials,
// fragments and query strings are not valid bases for pinned file paths.
func ValidModelMirror(mirror string) bool {
	mirror = strings.TrimSpace(mirror)
	if mirror == "auto" || mirror == "" {
		return true
	}
	if !strings.Contains(mirror, "://") {
		mirror = "https://" + mirror
	}
	parsed, err := url.Parse(mirror)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") &&
		parsed.Hostname() != "" && parsed.User == nil && parsed.RawQuery == "" &&
		!parsed.ForceQuery && parsed.Fragment == ""
}

func modelOrigins(mirror string) []string {
	mirror = strings.TrimSpace(mirror)
	if mirror == "" || mirror == "auto" {
		return modelMirrors
	}
	if !strings.Contains(mirror, "://") {
		mirror = "https://" + mirror
	}
	return []string{strings.TrimRight(mirror, "/")}
}

func modelFileURL(origin, rel string) string {
	return modelFileURLOf(modelDefinition{Repo: modelRepo, Revision: modelRevision}, origin, rel)
}

func modelFileURLOf(definition modelDefinition, origin, rel string) string {
	// rel comes from filepath.Join (variantFileName) and carries \ on Windows;
	// the resolve endpoint expects a slash-separated repo path.
	return origin + "/" + definition.Repo + "/resolve/" + definition.Revision + "/" + filepath.ToSlash(rel)
}

func (m *Manager) modelMirrorSetting(explicit string) string {
	if strings.TrimSpace(explicit) != "" {
		return explicit
	}
	return m.Preferences().ModelMirror
}

func expandTilde(p string) (string, error) {
	if rest, ok := strings.CutPrefix(p, "~/"); ok {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, rest), nil
	}
	return p, nil
}

// DownloadModel keeps the legacy default when no engine is provided.
func (m *Manager) DownloadModel(ctx context.Context, model, variant, mirror string) error {
	return m.DownloadModelForEngine(ctx, model, variant, mirror, "")
}

type plannedModelFile struct {
	spec modelFileSpec
	ort  bool
}

func m3DownloadPlan(variant, engine string, ownedCPU bool) ([]plannedModelFile, error) {
	spec, ok := modelFileSpecs[variant]
	if variant == "baseline" {
		variant, spec, ok = "quantized", modelFileSpecs["quantized"], true
	} else if !ok {
		return nil, errors.New("未知模型文件: " + variant)
	}
	files := []plannedModelFile{{spec: spec}}
	if variant == "tokenizer" {
		return files, nil
	}
	// A requested weight is deployable only with its tokenizer. Preserve the
	// legacy ONNX file until Runtime can plan the target's ORT-only selection
	// independently of the currently active model/engine.
	files = append([]plannedModelFile{{spec: modelFileSpecs["tokenizer"]}}, files...)
	if ownedCPU && (engine == "cpu" || engine == "") && variant == "quantized" {
		files = append(files, plannedModelFile{spec: m3CPUContainer, ort: true})
	}
	return files, nil
}

// DownloadModelForEngine downloads pinned prerequisites for one explicit
// engine/variant; it does not change Runtime's selected model or engine.
func (m *Manager) DownloadModelForEngine(ctx context.Context, model, variant, mirror, engine string) error {
	switch engine {
	case "", "cpu", "gpu", "npu":
	default:
		return errors.New("未知的模型下载引擎")
	}
	dir, definition, err := m.modelDirFor(model)
	if err != nil {
		return err
	}
	var files []plannedModelFile
	if definition.Legacy {
		files, err = m3DownloadPlan(variant, engine, m3UsesCPUContainer())
		if err != nil {
			return err
		}
	} else {
		if variant == "baseline" {
			variant = definition.Variants[0].Name
		}
		spec, specErr := definition.modelFileSpecFor(variant)
		if specErr != nil {
			return specErr
		}
		files = []plannedModelFile{{spec: definition.Tokenizer}, {spec: spec}}
	}
	mirror = m.modelMirrorSetting(mirror)
	if !ValidModelMirror(mirror) {
		return errors.New("模型镜像地址无效")
	}
	origins := modelOrigins(mirror)
	for _, file := range files {
		spec := file.spec
		dest := filepath.Join(dir, filepath.FromSlash(spec.rel))
		var urls []string
		if file.ort {
			urls = m3CPUContainerSources()
		} else {
			urls = make([]string, len(origins))
			for i, origin := range origins {
				urls[i] = modelFileURLOf(definition, origin, spec.rel)
			}
		}
		if err := m.downloadModelFromSources(ctx, urls, dest, spec); err != nil {
			return err
		}
	}
	return nil
}

func m3CPUContainerSources() []string {
	return []string{
		mirrorURL(m3CPUContainerURL),
		"https://gh-proxy.org/" + m3CPUContainerURL,
		m3CPUContainerURL,
	}
}

func (m *Manager) downloadModelFromSources(ctx context.Context, urls []string, dest string, spec modelFileSpec) error {
	var failures error
	for _, url := range urls {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		err := m.downloadModelFrom(ctx, url, dest, spec)
		if err == nil || errors.Is(err, errCorruptModelCleanup) {
			return err
		}
		failures = errors.Join(failures, err)
	}
	return failures
}

func m3UsesCPUContainer() bool {
	if choice, set := os.LookupEnv("RSRS_CPU_OWNED_ORT_EXPERIMENT"); set && choice == "0" {
		return false
	}
	_, profiling := os.LookupEnv("RSRS_ORT_PROFILE")
	return !profiling
}

func (m *Manager) cpuContainerPresent(dir string) bool {
	if !m3UsesCPUContainer() {
		return true
	}
	relative := filepath.FromSlash(m3CPUContainer.rel)
	// Runtime resolves its user cache ahead of an explicitly configured model directory.
	info, err := os.Lstat(filepath.Join(m.ModelDir(), relative))
	if errors.Is(err, os.ErrNotExist) {
		info, err = os.Lstat(filepath.Join(dir, relative))
	}
	return err == nil && info.Mode().IsRegular() && info.Size() == m3CPUContainer.size
}

func modelFileValid(path string, spec modelFileSpec) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular() && info.Size() == spec.size && fileHashMust(path) == spec.sha256
}

func fileHashMust(path string) string {
	sum, err := fileHash(path)
	if err != nil {
		return ""
	}
	return sum
}
