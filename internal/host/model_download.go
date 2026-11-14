package host

// Multi-threaded downloads for BGE-M3 weights: the client fetches the pinned
// files itself with parallel Range chunks and SHA256 verification, then the
// CLI's installer sees valid files and skips its own single-stream download.
// URLs and hashes mirror respire-cli's cli/src/model_install.rs.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
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
	"tokenizer": {rel: "tokenizer.json", sha256: tokenizerSHA, size: 17433231},
	"quantized": {rel: "onnx/model_quantized.onnx", sha256: "0826f8c1ab9edf1801db86c61919d4d108e8bfc0b809ec823ad366882ff0b77d", size: 569694530},
	"fp16":      {rel: "onnx/model_fp16.onnx", sha256: "4f1a646a3d4f39985589e9991a717044ede8278617fe55e3d246838bc05055e9", size: 1133992936},
	"int8":      {rel: "onnx/model_int8.onnx", sha256: "a206e10e995aa2a833924bcd725ba5dd6c3425cd34bac3cf2b5677cd2a1c51d6", size: 568456694},
	"uint8":     {rel: "onnx/model_uint8.onnx", sha256: "3222211565d0d5da2fe89e0c40856ed879e5c5dd3369fbbe685213469498e811", size: 568456765},
	"q4":        {rel: "onnx/model_q4.onnx", sha256: "4f6d9786712f8dbf00efbb08ffd819395ef05d860249e852b4efc16022f753a6", size: 1248237611},
	"q4f16":     {rel: "onnx/model_q4f16.onnx", sha256: "8305fdfa48b5702ecb4a6d84c1a52a4d1cb27d69880ff7f69d1a93d795864f25", size: 699932840},
}

var modelMirrors = []string{"https://hf-mirror.com", "https://hf-mirror.net", "https://huggingface.co"}

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

// modelClient streams multi-hundred-MiB chunks: no overall deadline (the
// request context governs). It shares the manager's outbound transport, so
// environment and system proxy settings apply to model downloads too.
func (m *Manager) modelClient() *http.Client {
	if m.modelHTTPClient != nil {
		return m.modelHTTPClient
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if m.proxy != nil {
		transport.Proxy = m.proxy.proxyFunc(func() string { return m.Preferences().ProxyMode })
	}
	return &http.Client{Transport: transport}
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

// modelMirrorSetting resolves the download source the same way the CLI does:
// an explicit choice wins, otherwise agent.json model_mirror, otherwise auto.
func (m *Manager) modelMirrorSetting(explicit string) string {
	if strings.TrimSpace(explicit) != "" {
		return explicit
	}
	agent := readAgentConfig(m.DataRoot)
	if mirror, _ := agent["model_mirror"].(string); mirror != "" {
		return mirror
	}
	return "auto"
}

// readAgentConfig reads the agent.json in the effective data dir (client.json
// data_dir when set, mirroring the CLI's data_dir resolution).
func readAgentConfig(dataRoot string) map[string]any {
	config := map[string]any{}
	if data, err := os.ReadFile(filepath.Join(dataRoot, "client.json")); err == nil {
		var parsed map[string]any
		if json.Unmarshal(data, &parsed) == nil {
			if dir, _ := parsed["data_dir"].(string); dir != "" {
				if expanded, err := expandTilde(dir); err == nil {
					dataRoot = expanded
				}
			}
		}
	}
	data, err := os.ReadFile(filepath.Join(dataRoot, "agent.json"))
	if err != nil {
		return config
	}
	json.Unmarshal(data, &config)
	return config
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

// DownloadModel fetches one pinned file into the model's dir. For m3 the
// variant is one of the legacy keys or "baseline" (tokenizer + quantized
// weights, the first-run set); profile models download tokenizer.json plus
// the chosen onnx variant from their pinned repository.
func (m *Manager) DownloadModel(ctx context.Context, model, variant, mirror string) error {
	dir, definition, err := m.modelDirFor(model)
	if err != nil {
		return err
	}
	var files []modelFileSpec
	if definition.Legacy {
		switch variant {
		case "baseline":
			files = []modelFileSpec{modelFileSpecs["tokenizer"], modelFileSpecs["quantized"]}
		default:
			spec, ok := modelFileSpecs[variant]
			if !ok {
				return errors.New("未知模型文件: " + variant)
			}
			files = []modelFileSpec{spec}
		}
	} else {
		if variant == "baseline" {
			variant = "quantized"
		}
		spec, specErr := definition.modelFileSpecFor(variant)
		if specErr != nil {
			return specErr
		}
		files = []modelFileSpec{definition.Tokenizer, spec}
	}
	origins := modelOrigins(m.modelMirrorSetting(mirror))
	for _, spec := range files {
		dest := filepath.Join(dir, filepath.FromSlash(spec.rel))
		if modelFileValid(dest, spec) {
			continue
		}
		var lastErr error
		for _, origin := range origins {
			err := m.downloadModelFrom(ctx, modelFileURLOf(definition, origin, spec.rel), dest, spec)
			if err == nil {
				break
			}
			lastErr = err
			if ctx.Err() != nil {
				return ctx.Err()
			}
		}
		if lastErr != nil {
			return lastErr
		}
	}
	return nil
}

func modelFileValid(path string, spec modelFileSpec) bool {
	info, err := os.Stat(path)
	return err == nil && info.Size() == spec.size && fileHashMust(path) == spec.sha256
}

func fileHashMust(path string) string {
	sum, err := fileHash(path)
	if err != nil {
		return ""
	}
	return sum
}

// downloadModelFrom downloads url into dest through parallel Range chunks
// (falling back to a single stream when the source ignores Range). Partial
// chunks stay on disk so a retry resumes instead of restarting.
func (m *Manager) downloadModelFrom(ctx context.Context, url, dest string, spec modelFileSpec) error {
	if modelFileValid(dest, spec) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
		return err
	}
	tmp := dest + ".download"
	workers := 8
	const minParallel = 32 << 20
	if spec.size < minParallel {
		workers = 1
	}
	chunk := (spec.size + int64(workers) - 1) / int64(workers)
	parts := make([]string, workers)
	for i := range parts {
		parts[i] = fmt.Sprintf("%s.p%d", tmp, i)
	}
	if err := m.downloadChunks(ctx, url, tmp, parts, spec, chunk, workers); err != nil {
		return err
	} // Merge, verify, publish.
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	hasher := sha256.New()
	var merged int64
	for _, part := range parts {
		data, err := os.ReadFile(part)
		if err != nil {
			out.Close()
			return err
		}
		if _, err = out.Write(data); err != nil {
			out.Close()
			return err
		}
		hasher.Write(data)
		merged += int64(len(data))
	}
	if err = out.Close(); err != nil {
		return err
	}
	if merged != spec.size || hex.EncodeToString(hasher.Sum(nil)) != spec.sha256 {
		_ = os.Remove(tmp)
		return fmt.Errorf("下载的 %s 大小或校验和不匹配", filepath.Base(dest))
	}
	_ = os.Remove(dest)
	if err = os.Rename(tmp, dest); err != nil {
		return err
	}
	for _, part := range parts {
		_ = os.Remove(part)
	}
	m.progress("模型文件校验通过", 0, 0)
	return nil
}

func (m *Manager) downloadChunks(ctx context.Context, url, tmp string, parts []string, spec modelFileSpec, chunk int64, workers int) error {
	var done atomic.Int64
	errs := make([]error, workers)
	base := filepath.Base(spec.rel)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		start := int64(i) * chunk
		end := start + chunk - 1
		if end >= spec.size {
			end = spec.size - 1
		}
		if start > end {
			break
		}
		wg.Add(1)
		go func(i int, start, end int64) {
			defer wg.Done()
			errs[i] = m.downloadChunk(ctx, url, parts[i], base, start, end, &done, spec.size)
		}(i, start, end)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// downloadChunk fetches one Range into its part file, resuming from the part's
// current size and retrying with fresh requests until complete.
func (m *Manager) downloadChunk(ctx context.Context, url, part, base string, start, end int64, done *atomic.Int64, total int64) error {
	partTotal := end - start + 1
	var have int64
	if info, err := os.Stat(part); err == nil {
		have = info.Size()
	}
	if have > partTotal {
		_ = os.Remove(part)
		have = 0
	}
	if have >= partTotal {
		done.Add(partTotal)
		return nil
	}
	done.Add(have)
	file, err := os.OpenFile(part, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	var lastReport int64
	report := func() {
		if now := done.Load(); now-lastReport >= 8<<20 || now == total {
			lastReport = now
			m.progress("下载 "+base, now, total)
		}
	}
	for attempt := 0; attempt < 6; attempt++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		offset := start + have
		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, end))
		res, err := m.modelClient().Do(req)
		if err != nil {
			continue
		}
		if res.StatusCode == 200 && partTotal != total {
			// Source ignored Range: only safe as a single-stream whole file.
			res.Body.Close()
			return errors.New("下载源不支持分段并行")
		}
		if res.StatusCode != 206 && res.StatusCode != 200 {
			res.Body.Close()
			continue
		}
		buf := make([]byte, 256<<10)
		for {
			n, readErr := res.Body.Read(buf)
			if n > 0 {
				if _, writeErr := file.Write(buf[:n]); writeErr != nil {
					res.Body.Close()
					return writeErr
				}
				have += int64(n)
				done.Add(int64(n))
				report()
			}
			if readErr == io.EOF {
				res.Body.Close()
				if have >= partTotal {
					return nil
				}
				break // short body: retry from current offset
			}
			if readErr != nil {
				res.Body.Close()
				break
			}
		}
	}
	if have >= partTotal {
		return nil
	}
	return fmt.Errorf("分段下载未完成（%d / %d）", have, partTotal)
}
