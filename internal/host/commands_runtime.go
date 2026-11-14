package host

// Onnx model variants accepted by `rsrs model engine --model` and
// `rsrs model install-m3 --model` (cli v1.0.20-dev.5 MODEL_VARIANTS).
var modelVariants = []string{"quantized", "fp16", "int8", "uint8", "q4", "q4f16"}

var runtimeCommands = map[string]commandSpec{
	"capabilities":          {words: []string{"capabilities"}},
	"status":                {words: []string{"status"}},
	"doctor":                {words: []string{"doctor"}},
	"memory_revision":       {words: []string{"memory-revision"}},
	"sync":                  {words: []string{"sync"}, write: true},
	"sync_conflicts":        {words: []string{"sync-conflicts"}, args: []argument{flag("all", "--all")}},
	"model_engine":          {words: []string{"model", "engine"}},
	"model_engine_set":      {words: []string{"model", "engine"}, write: true, args: []argument{choice("engine", "", "cpu", "gpu", "npu"), choice("model", "--model", modelVariants...)}},
	"model_engine_apply":    {method: "model.apply-engine", write: true, args: []argument{choice("engine", "", "cpu", "gpu", "npu"), choice("model", "--model", modelVariants...)}},
	"model_prepare":         {method: "model.prepare", write: true, args: []argument{pos("mirror")}},
	"model_progress":        {method: "model.control", args: []argument{pos("id")}},
	"model_cancel":          {method: "model.control", write: true, args: []argument{pos("id")}},
	"model_install":         {words: []string{"model", "install-m3"}, host: true, write: true, args: []argument{opt("mirror", "--mirror"), choice("model", "--model", modelVariants...)}},
	"model_uninstall":       {words: []string{"model", "uninstall-m3"}, host: true, write: true},
	"model_probe":           {words: []string{"model", "probe"}, args: []argument{opt("text", "--text")}},
	"model_install_engines": {words: []string{"model", "install-engines"}, host: true, write: true},
	"index_prepare":         {method: "index.prepare", write: true},
	"index_rebuild":         {words: []string{"reembed"}, write: true},
}
