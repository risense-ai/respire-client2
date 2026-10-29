package host

var runtimeCommands = map[string]commandSpec{
	"status":                {words: []string{"status"}},
	"doctor":                {words: []string{"doctor"}},
	"memory_revision":       {words: []string{"memory-revision"}},
	"sync":                  {words: []string{"sync"}, write: true},
	"sync_conflicts":        {words: []string{"sync-conflicts"}, args: []argument{flag("all", "--all")}},
	"model_engine":          {words: []string{"model", "engine"}},
	"model_engine_set":      {words: []string{"model", "engine"}, write: true, args: []argument{choice("engine", "", "cpu", "gpu", "npu")}},
	"model_prepare":         {method: "model.prepare", write: true, args: []argument{pos("mirror")}},
	"model_progress":        {method: "model.control", args: []argument{pos("id")}},
	"model_cancel":          {method: "model.control", write: true, args: []argument{pos("id")}},
	"model_install":         {words: []string{"model", "install-m3"}, write: true, args: []argument{opt("mirror", "--mirror")}},
	"model_uninstall":       {words: []string{"model", "uninstall-m3"}, write: true},
	"model_probe":           {words: []string{"model", "probe"}, args: []argument{opt("text", "--text")}},
	"model_install_engines": {words: []string{"model", "install-engines"}, host: true, write: true},
	"index_prepare":         {method: "index.prepare", write: true},
	"index_rebuild":         {words: []string{"reembed"}, write: true},
}
