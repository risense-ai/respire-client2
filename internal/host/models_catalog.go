package host

// Pinned embedding model catalog. m3 keeps the CLI's legacy variant table and
// directory resolution; the profile models download into models/<id> and
// register through the runtime's embedding-profile registry (model.configure),
// then activate with model.switch. Sizes and SHA-256 mirror the pinned HF
// revisions so activation can verify resources without re-downloading.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"time"
)

type modelDefinition struct {
	ID          string
	Label       string
	Repo        string
	Revision    string
	Dimensions  int
	MaxTokens   int
	QueryPrefix string
	Tokenizer   modelFileSpec
	// Variants in display order; profile models activate quantized.
	Variants []modelVariantFile
	// Hashes keyed by variant name.
	Hashes map[string]string
	// Legacy models use the CLI variant table (model.apply-engine) and the
	// bge-m3 directory resolution; profile models use the profile registry.
	Legacy bool
}

var modelCatalog = []modelDefinition{
	{
		ID: "m3", Label: "BGE-M3 · 1024 维 · 多语",
		Repo: "Xenova/bge-m3", Revision: modelRevision,
		Dimensions: 1024, MaxTokens: 512, Legacy: true,
		Tokenizer: modelFileSpec{rel: "tokenizer.json", sha256: tokenizerSHA, size: 17433231},
		Variants:  modelVariantFiles,
		Hashes: map[string]string{
			"quantized": "0826f8c1ab9edf1801db86c61919d4d108e8bfc0b809ec823ad366882ff0b77d",
			"fp16":      "4f1a646a3d4f39985589e9991a717044ede8278617fe55e3d246838bc05055e9",
			"int8":      "a206e10e995aa2a833924bcd725ba5dd6c3425cd34bac3cf2b5677cd2a1c51d6",
			"uint8":     "3222211565d0d5da2fe89e0c40856ed879e5c5dd3369fbbe685213469498e811",
			"q4":        "4f6d9786712f8dbf00efbb08ffd819395ef05d860249e852b4efc16022f753a6",
			"q4f16":     "8305fdfa48b5702ecb4a6d84c1a52a4d1cb27d69880ff7f69d1a93d795864f25",
		},
	},
	{
		ID: "bge-base-zh-v1.5", Label: "BGE-base-zh-v1.5 · 768 维 · 中文",
		Repo: "Xenova/bge-base-zh-v1.5", Revision: "71e50dc531959f9e04ebf190ea25b00261a0a186",
		Dimensions: 768, MaxTokens: 512,
		QueryPrefix: "为这个句子生成表示以用于检索相关文章：",
		Tokenizer:   modelFileSpec{rel: "tokenizer.json", sha256: "7dfbf1966ebf99d471c3796e9b457329d2b2182b817e144f1e904b957745c839", size: 439124},
		Variants: []modelVariantFile{
			{"quantized", 102868746},
			{"fp16", 203679052},
			{"int8", 102407793},
			{"uint8", 102407824},
			{"q4", 120342974},
			{"q4f16", 81549931},
		},
		Hashes: map[string]string{
			"quantized": "b665f3bba56c3119bc76ba131ebcc544d720a7408cb11581bdf354aaa0198d43",
			"fp16":      "9ed3f10ed06aafe0320567019d18fdec255339350525a33ae18f85f4def288f0",
			"int8":      "c11bdcfad37eaf53e43362885cbe5fad1b44d5b704da753a04f8c0b2f7768064",
			"uint8":     "5e2f9e9f07472751e0a5a0b21bcfa49cb0ac1eaab094784a3f30425a8bcc1d80",
			"q4":        "dab1bb68aaf39bbfa4ef7d218091174f01cd25781a07f36c654a2e3037d4376a",
			"q4f16":     "8278a219a9ae056860dcde7ce8241bee057a6ab0243454c85facf4d6e8325777",
		},
	},
	{
		ID: "bge-m3-distill-8l", Label: "BGE-M3-Distill-8L · 1024 维 · 轻量多语",
		Repo: "xnohat/bge-m3-distill-8l", Revision: "bbf72a07734915b3fa813cadc46f11414d2b4c25",
		Dimensions: 1024, MaxTokens: 512,
		Tokenizer: modelFileSpec{rel: "tokenizer.json", sha256: "e4f7e21bec3fb0044ca0bb2d50eb5d4d8c596273c422baef84466d2c73748b9c", size: 17083053},
		Variants: []modelVariantFile{
			{"quantized", 366226833},
			{"fp16", 730630097},
			{"int8", 366226833},
			{"uint8", 366226833},
			{"q4", 1121229266},
			{"q4f16", 585933772},
		},
		Hashes: map[string]string{
			"quantized": "05e237aa057bc3f21cfeb48651a768f9ae8001907132b29fc27ae58aae27a052",
			"fp16":      "34a8df87404689e366c8bdfab6b094efedf4be3c1ae8f1125cbe02ec65425aa0",
			"int8":      "05e237aa057bc3f21cfeb48651a768f9ae8001907132b29fc27ae58aae27a052",
			"uint8":     "f961325ab451ff54528c48710975790465055f40b13dcb715399f0083e0f1e2d",
			"q4":        "d0cc972bcbc78b0df07ea8913293987be9a783304aa70c5620103fc4313a8f9c",
			"q4f16":     "6f995eae32f9afd8ebee450569ced024c965ecad478e910c752b992fa53fc85e",
		},
	},
}

func modelDefinitionByID(id string) (modelDefinition, error) {
	for _, definition := range modelCatalog {
		if definition.ID == id {
			return definition, nil
		}
	}
	return modelDefinition{}, errors.New("未知模型: " + id)
}

// ProfileDir is the on-disk weight directory for profile models; the runtime
// profile points at these files (model.configure resources).
func (m *Manager) ProfileDir(model string) string {
	return filepath.Join(m.DataRoot, "models", model)
}

// modelFileSpecFor returns the download spec for one variant of a model.
func (definition modelDefinition) modelFileSpecFor(variant string) (modelFileSpec, error) {
	hash, ok := definition.Hashes[variant]
	if !ok {
		return modelFileSpec{}, errors.New("未知模型文件: " + variant)
	}
	for _, candidate := range definition.Variants {
		if candidate.Name == variant {
			return modelFileSpec{rel: variantFileName(variant), sha256: hash, size: candidate.Size}, nil
		}
	}
	return modelFileSpec{}, errors.New("未知模型文件: " + variant)
}

// ConfigurePayload builds the model.configure argument for a profile model.
func (definition modelDefinition) ConfigurePayload(variant, weightsPath string) (string, error) {
	if definition.Legacy {
		return "", errors.New("m3 使用内置变体表，无需注册档案")
	}
	weights, err := definition.modelFileSpecFor(variant)
	if err != nil {
		return "", err
	}
	payload := map[string]any{
		"model_profile": map[string]any{
			"id": definition.ID,
			"embedding": map[string]any{
				"model_id":          definition.Repo,
				"revision":          definition.Revision,
				"dimensions":        definition.Dimensions,
				"max_tokens":        definition.MaxTokens,
				"pooling":           "cls",
				"normalize":         true,
				"query_prefix":      definition.QueryPrefix,
				"document_prefix":   "",
				"pad_token_id":      0,
				"pad_to_max_tokens": false,
			},
			"onnx": map[string]any{
				"input_ids":      "input_ids",
				"attention_mask": "attention_mask",
				"token_type_ids": "token_type_ids",
				"output":         "last_hidden_state",
			},
			"weights": map[string]any{
				"path": weightsPath, "size": weights.size, "sha256": weights.sha256,
			},
			"tokenizer": map[string]any{
				"path": filepath.Join(filepath.Dir(weightsPath), "..", "tokenizer.json"), "size": definition.Tokenizer.size, "sha256": definition.Tokenizer.sha256,
			},
		},
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

// runtimeRPC calls the resident runtime loopback /api/rpc with the v:1
// envelope the host RPC documents (model.catalog / configure / switch / ...).
func (m *Manager) runtimeRPC(ctx context.Context, method string, args []string) (json.RawMessage, error) {
	if args == nil {
		args = []string{}
	}
	body := map[string]any{
		"v":      1,
		"id":     fmt.Sprintf("client2-model-%d", time.Now().UnixNano()),
		"method": method,
		"args":   args,
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", m.Endpoint+"/api/rpc", bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := m.localClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	var reply struct {
		OK       bool            `json:"ok"`
		Exit     *int            `json:"exit"`
		Envelope json.RawMessage `json:"envelope"`
		Errors   []string        `json:"errors"`
	}
	if err := json.Unmarshal(raw, &reply); err != nil {
		return nil, fmt.Errorf("runtime RPC 响应无法解析: %w", err)
	}
	if !reply.OK {
		message := "runtime RPC 调用失败"
		if len(reply.Errors) > 0 {
			message = reply.Errors[0]
		}
		return nil, errors.New(message)
	}
	if len(reply.Envelope) == 0 {
		return json.RawMessage("{}"), nil
	}
	var envelope struct {
		Status string   `json:"status"`
		Errors []string `json:"errors"`
	}
	_ = json.Unmarshal(reply.Envelope, &envelope)
	if envelope.Status == "fail" {
		message := "runtime 拒绝了模型操作"
		if len(envelope.Errors) > 0 {
			message = envelope.Errors[0]
		}
		return nil, errors.New(message)
	}
	return reply.Envelope, nil
}

// ModelCatalogRPC reports the registry plus the active selection and any
// pending switch task.
func (m *Manager) ModelCatalogRPC(ctx context.Context) (json.RawMessage, error) {
	return m.runtimeRPC(ctx, "model.catalog", nil)
}

// ModelConfigure registers a profile model's resources with the runtime.
func (m *Manager) ModelConfigure(ctx context.Context, model, variant string) error {
	definition, err := modelDefinitionByID(model)
	if err != nil {
		return err
	}
	if variant == "" {
		variant = "quantized"
	}
	payload, err := definition.ConfigurePayload(variant, filepath.Join(m.ProfileDir(model), variantFileName(variant)))
	if err != nil {
		return err
	}
	_, err = m.runtimeRPC(ctx, "model.configure", []string{payload})
	return err
}

// ModelSwitch activates a model profile with the chosen engine.
func (m *Manager) ModelSwitch(ctx context.Context, model, engine, variant string) error {
	if _, err := modelDefinitionByID(model); err != nil {
		return err
	}
	if variant == "" {
		variant = "quantized"
	}
	selection, err := json.Marshal(map[string]any{
		"model": model, "backend": "onnx", "engine": engine, "variant": variant,
	})
	if err != nil {
		return err
	}
	_, err = m.runtimeRPC(ctx, "model.switch", []string{string(selection)})
	return err
}

// ModelProgress reports the pending switch task and active selection.
func (m *Manager) ModelProgress(ctx context.Context) (json.RawMessage, error) {
	return m.runtimeRPC(ctx, "model.progress", nil)
}
