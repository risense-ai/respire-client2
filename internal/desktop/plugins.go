package desktop

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

type pluginCommand struct {
	name string
	args []string
	dir  string
}

type pluginOutput struct {
	bytes.Buffer
	overflow bool
}

func (b *pluginOutput) Write(data []byte) (int, error) {
	if b.Len()+len(data) > 64<<10 {
		b.overflow = true
		data = data[:max(0, 64<<10-b.Len())]
	}
	return b.Buffer.Write(data)
}

func (b *pluginOutput) String() string {
	if b.overflow {
		return b.Buffer.String() + "\n输出超过 64 KiB，已截断"
	}
	return b.Buffer.String()
}

type pluginStatus struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Repo     string `json:"repo"`
	Source   bool   `json:"source"`
	Command  bool   `json:"command"`
	Path     string `json:"path"`
	Auto     bool   `json:"auto"`
	Manual   string `json:"manual"`
	Profiles string `json:"profiles"`
}

func pluginAuto(id string) bool {
	switch id {
	case "respire-zcode", "respire-kimi":
		return false
	default:
		return true
	}
}

type pluginSpec struct {
	name    string
	repo    string
	command string
	manual  string
}

var pluginSpecs = []pluginSpec{
	{"magpie", "respire-magpie", "magpie", "在 Magpie 插件页安装本机源目录。"},
	{"dsh", "respire-dsh", "dsh", "默认安装到 DeepSeek Harness desktop profile。"},
	{"workbuddy", "respire-workbuddy", "bash", "WorkBuddy 安装脚本会调用本机 codebuddy CLI。"},
	{"codex", "respire-codex", "codex", "安装后在 Codex /hooks 中确认信任钩子。"},
	{"qoder", "respire-qoder", "qoder", "安装后重启 Qoder 会话。"},
	{"hermes", "respire-hermes", "bash", "Hermes 仅支持 Linux 与 Apple Silicon macOS。"},
	{"zcode", "respire-zcode", "", "ZCode 需在插件市场界面添加本机源目录。"},
	{"dimcode", "respire-dimcode", "dim", "安装后重启 Dim 会话。"},
	{"pi", "respire-pi", "pi", "安装后重启 Pi 或执行 /reload。"},
	{"kimi", "respire-kimi", "", "Kimi Code 需在其 /plugins 界面安装本机源目录。"},
}

func pluginRepo(id string, mirror bool) string {
	url := "https://github.com/risense-ai/" + id
	if mirror {
		url = "https://gh-proxy.com/" + url
	}
	return url
}

func pluginByID(id string) (pluginSpec, bool) {
	for _, item := range pluginSpecs {
		if item.repo == id {
			return item, true
		}
	}
	return pluginSpec{}, false
}

func (d *desktop) pluginPath(repo string) string {
	return filepath.Join(d.m.Root, "plugins", repo)
}

func (d *desktop) PluginStatuses(ctx context.Context) []pluginStatus {
	result := make([]pluginStatus, 0, len(pluginSpecs))
	for _, spec := range pluginSpecs {
		item := pluginStatus{
			ID:     spec.repo,
			Name:   spec.name,
			Repo:   "https://github.com/risense-ai/" + spec.repo,
			Path:   d.pluginPath(spec.repo),
			Source: pluginSourceExists(d.pluginPath(spec.repo)),
			Auto:   pluginAuto(spec.repo),
			Manual: spec.manual,
		}
		if spec.command != "" {
			path, err := exec.LookPath(spec.command)
			item.Command = err == nil && path != ""
		}
		if spec.repo == "respire-dsh" {
			item.Profiles = "desktop"
		}
		result = append(result, item)
	}
	return result
}

func (d *desktop) InstallPlugin(ctx context.Context, id string, mirror bool) (string, error) {
	spec, ok := pluginByID(id)
	if !ok {
		return "", fmt.Errorf("未知插件")
	}
	path := d.pluginPath(id)
	if err := updatePluginSource(ctx, path, pluginRepo(id, mirror)); err != nil {
		return "", err
	}
	commands, err := d.pluginCommands(spec, path)
	if err != nil {
		return "", err
	}
	var out pluginOutput
	for _, command := range commands {
		if err = runPluginCommand(ctx, command, &out); err != nil {
			return "", err
		}
	}
	message := "插件安装请求已完成"
	if text := strings.TrimSpace(out.String()); text != "" {
		message += "：" + text
	}
	return message, nil
}

func (d *desktop) pluginCommands(spec pluginSpec, path string) ([]pluginCommand, error) {
	switch spec.repo {
	case "respire-magpie":
		return []pluginCommand{{name: spec.command, args: []string{"plugin", "add", path}}}, nil
	case "respire-dsh":
		return []pluginCommand{{name: spec.command, args: []string{"plugin", "--profile", "desktop", "add", "@rsrsai/dsh"}}}, nil
	case "respire-workbuddy":
		return []pluginCommand{{name: "bash", args: []string{"install.sh"}, dir: path}}, nil
	case "respire-codex":
		return []pluginCommand{
			{name: spec.command, args: []string{"plugin", "marketplace", "add", "risense-ai/respire-codex", "--ref", "main"}},
			{name: spec.command, args: []string{"plugin", "add", "respire@rsrsai"}},
		}, nil
	case "respire-qoder":
		return []pluginCommand{
			{name: spec.command, args: []string{"plugins", "marketplace", "add", path}},
			{name: spec.command, args: []string{"plugins", "install", "respire-qoder@risense"}},
		}, nil
	case "respire-hermes":
		return []pluginCommand{{name: "bash", args: []string{"scripts/install.sh"}, dir: path}}, nil
	case "respire-dimcode":
		return []pluginCommand{
			{name: spec.command, args: []string{"plugin", "marketplace", "add", filepath.Join(path, ".agents", "plugins", "marketplace.json")}},
			{name: spec.command, args: []string{"plugin", "install", "respire@risense"}},
		}, nil
	case "respire-pi":
		return []pluginCommand{{name: spec.command, args: []string{"install", "npm:@rsrsai/pi"}}}, nil
	default:
		return nil, fmt.Errorf("%s 需要在宿主插件界面安装；源目录已准备：%s", spec.name, path)
	}
}

func runPluginCommand(ctx context.Context, command pluginCommand, out *pluginOutput) error {
	exe, err := exec.LookPath(command.name)
	if err != nil {
		return fmt.Errorf("未找到 %s", command.name)
	}
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		extension := strings.ToLower(filepath.Ext(exe))
		if extension == ".cmd" || extension == ".bat" {
			cmd = exec.CommandContext(ctx, "cmd", "/C", exe)
			cmd.Args = append(cmd.Args, command.args...)
		}
	}
	if cmd == nil {
		cmd = exec.CommandContext(ctx, exe, command.args...)
	}
	cmd.Dir = command.dir
	if out != nil {
		cmd.Stdout = out
		cmd.Stderr = out
	}
	if err = cmd.Run(); err != nil {
		if out != nil && out.Len() > 0 {
			text := strings.TrimSpace(out.String())
			if text != "" {
				return fmt.Errorf("%w：%s", err, text)
			}
		}
		return err
	}
	return nil
}

func pluginSourceExists(path string) bool {
	_, err := os.Stat(filepath.Join(path, ".git"))
	return err == nil
}

func updatePluginSource(ctx context.Context, path, repo string) error {
	if pluginSourceExists(path) {
		if err := runPluginCommand(ctx, pluginCommand{name: "git", args: []string{"fetch", "--depth", "1", "origin", "main"}, dir: path}, nil); err != nil {
			return fmt.Errorf("更新插件源失败: %w", err)
		}
		if err := runPluginCommand(ctx, pluginCommand{name: "git", args: []string{"merge", "--ff-only", "FETCH_HEAD"}, dir: path}, nil); err != nil {
			return fmt.Errorf("更新插件源失败，请检查目录是否被本机修改: %w", err)
		}
		return nil
	}
	return runPluginCommand(ctx, pluginCommand{name: "git", args: []string{"clone", "--depth", "1", "--branch", "main", repo, path}}, nil)
}
