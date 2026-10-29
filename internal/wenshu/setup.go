package wenshu

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"
)

type LocalSettings struct {
	BrowserPath    string `json:"browser_path"`
	DownloadSource string `json:"download_source"`
}

func loadSettings(dir string) (LocalSettings, error) {
	var s LocalSettings
	b, e := os.ReadFile(filepath.Join(dir, "config.json"))
	if os.IsNotExist(e) {
		return s, nil
	}
	if e != nil {
		return s, e
	}
	e = json.Unmarshal(b, &s)
	return s, e
}

type SetupOptions struct {
	Clients        string
	Browser        string
	DownloadSource string
	ConfigPath     string
}
type clientTarget struct{ ID, Name, Path, Format string }

func clientTargets(home, goos, appData string) []clientTarget {
	claude := filepath.Join(home, ".config", "Claude", "claude_desktop_config.json")
	if goos == "darwin" {
		claude = filepath.Join(home, "Library", "Application Support", "Claude", "claude_desktop_config.json")
	}
	if goos == "windows" {
		if appData == "" {
			appData = filepath.Join(home, "AppData", "Roaming")
		}
		claude = filepath.Join(appData, "Claude", "claude_desktop_config.json")
	}
	return []clientTarget{{"codex", "Codex", filepath.Join(home, ".codex", "config.toml"), "toml"}, {"claude-desktop", "Claude Desktop", claude, "json"}, {"claude-code", "Claude Code", filepath.Join(home, ".claude.json"), "json"}, {"cursor", "Cursor", filepath.Join(home, ".cursor", "mcp.json"), "json"}, {"windsurf", "Windsurf", filepath.Join(home, ".codeium", "windsurf", "mcp_config.json"), "json"}, {"gemini", "Gemini CLI", filepath.Join(home, ".gemini", "settings.json"), "json"}, {"generic", "通用 MCP 配置", "", "json"}}
}
func choose(in *bufio.Reader, out io.Writer, prompt, def string) (string, error) {
	fmt.Fprintf(out, "%s [%s]: ", prompt, def)
	s, e := in.ReadString('\n')
	if e != nil && len(s) == 0 {
		return "", fmt.Errorf("请输入安装选项，或使用 setup --client codex")
	}
	s = strings.TrimSpace(s)
	if s == "" {
		s = def
	}
	return s, nil
}
func RunSetup(ctx context.Context, dataDir string, in io.Reader, out io.Writer, opt SetupOptions) error {
	home, e := os.UserHomeDir()
	if e != nil {
		return e
	}
	dir, e := filepath.Abs(dataDir)
	if e != nil {
		return e
	}
	if e = os.MkdirAll(dir, 0700); e != nil {
		return e
	}
	fmt.Fprintf(out, "\n裁判文书网 MCP %s · 安装设置\n\n", Version)
	reader := bufio.NewReader(in)
	targets := clientTargets(home, runtime.GOOS, os.Getenv("APPDATA"))
	interactive := opt.Clients == ""
	if interactive {
		fmt.Fprintln(out, "选择要接入的客户端（可用逗号分隔多个编号）：")
		for i, t := range targets {
			fmt.Fprintf(out, "  %d. %s\n", i+1, t.Name)
		}
		opt.Clients, e = choose(reader, out, "客户端", "1")
		if e != nil {
			return e
		}
	}
	selected := []clientTarget{}
	seen := map[string]bool{}
	for _, value := range strings.Split(strings.ReplaceAll(opt.Clients, "，", ","), ",") {
		value = strings.TrimSpace(value)
		var found *clientTarget
		for i, t := range targets {
			if value == t.ID || value == strconv.Itoa(i+1) {
				x := t
				found = &x
				break
			}
		}
		if found == nil {
			return fmt.Errorf("未知客户端 %q", value)
		}
		if !seen[found.ID] {
			selected = append(selected, *found)
			seen[found.ID] = true
		}
	}
	if opt.ConfigPath != "" {
		if len(selected) != 1 {
			return fmt.Errorf("config-path 需要只选择一个客户端")
		}
		selected[0].Path, e = filepath.Abs(opt.ConfigPath)
		if e != nil {
			return e
		}
	}
	if opt.DownloadSource == "" {
		opt.DownloadSource = os.Getenv("WENSHU_DOWNLOAD_SOURCE")
	}
	if opt.DownloadSource == "" {
		opt.DownloadSource = "cn"
	}
	browser := opt.Browser
	if browser == "" {
		if saved, err := loadSettings(dir); err == nil {
			if _, err = os.Stat(saved.BrowserPath); err == nil {
				browser = saved.BrowserPath
			}
		}
	}
	if browser == "" {
		choices := DetectBrowsers(dir)
		if len(choices) > 0 {
			index := 0
			if interactive && len(choices) > 1 {
				fmt.Fprintln(out, "\n选择登录浏览器：")
				for i, b := range choices {
					fmt.Fprintf(out, "  %d. %s — %s\n", i+1, b.Name, b.Path)
				}
				answer, err := choose(reader, out, "浏览器", "1")
				if err != nil {
					return err
				}
				index, err = strconv.Atoi(answer)
				if err != nil || index < 1 || index > len(choices) {
					return fmt.Errorf("浏览器编号无效")
				}
				index--
			}
			browser = choices[index].Path
		}
	}
	if browser == "" {
		if interactive {
			fmt.Fprintln(out, "\n将下载配套浏览器。下载源：1. 国内镜像（npmmirror）  2. 官方源")
			answer, err := choose(reader, out, "下载源", "1")
			if err != nil {
				return err
			}
			switch answer {
			case "1":
				opt.DownloadSource = "cn"
			case "2":
				opt.DownloadSource = "official"
			default:
				return fmt.Errorf("下载源编号无效")
			}
		}
		browser, e = DownloadBrowser(ctx, dir, opt.DownloadSource, out)
		if e != nil {
			return e
		}
	} else {
		browser, e = normalizeBrowser(browser)
		if e != nil {
			return e
		}
	}
	settings := LocalSettings{BrowserPath: browser, DownloadSource: opt.DownloadSource}
	if e = atomicWrite(filepath.Join(dir, "config.json"), []byte(jsonText(settings))); e != nil {
		return e
	}
	exe, e := installExecutable(dir)
	if e != nil {
		return e
	}
	fmt.Fprintf(out, "\n程序：%s\n浏览器：%s\n数据目录：%s\n", exe, browser, dir)
	// Check the actual MCP handshake before writing any client configuration.
	count, e := checkInstalledMCP(ctx, exe, dir)
	if e != nil {
		return fmt.Errorf("MCP 启动失败: %w", e)
	}
	fmt.Fprintf(out, "MCP 已就绪，已加载 %d 个工具。\n", count)
	for _, t := range selected {
		if t.ID == "generic" && t.Path == "" {
			t.Path = filepath.Join(dir, "wenshu-mcp-config.json")
		}
		backup, e := mergeClientConfig(t.Path, t.Format, exe, dir)
		if e != nil {
			return fmt.Errorf("写入 %s 配置失败: %w", t.Name, e)
		}
		fmt.Fprintf(out, "已接入 %s：%s\n", t.Name, t.Path)
		if backup != "" {
			fmt.Fprintf(out, "原配置备份：%s\n", backup)
		}
	}
	fmt.Fprintln(out, "\n安装完成。重载客户端 MCP 服务，然后对 AI 说：帮我登录裁判文书网。")
	return nil
}
func installExecutable(dir string) (string, error) {
	source, e := os.Executable()
	if e != nil {
		return "", e
	}
	source, e = filepath.EvalSymlinks(source)
	if e != nil {
		return "", e
	}
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	dest := filepath.Join(dir, "bin", "wenshu-mcp-"+Version+suffix)
	if filepath.Clean(source) == filepath.Clean(dest) {
		return dest, nil
	}
	if e = os.MkdirAll(filepath.Dir(dest), 0700); e != nil {
		return "", e
	}
	in, e := os.Open(source)
	if e != nil {
		return "", e
	}
	defer in.Close()
	tmp, e := os.CreateTemp(filepath.Dir(dest), ".install-*")
	if e != nil {
		return "", e
	}
	defer os.Remove(tmp.Name())
	if _, e = io.Copy(tmp, in); e != nil {
		tmp.Close()
		return "", e
	}
	if e = tmp.Chmod(0755); e != nil {
		tmp.Close()
		return "", e
	}
	if e = tmp.Close(); e != nil {
		return "", e
	}
	if e = os.Rename(tmp.Name(), dest); e != nil {
		dest = filepath.Join(dir, "bin", fmt.Sprintf("wenshu-mcp-%s-%d%s", Version, time.Now().UnixNano(), suffix))
		if e = os.Rename(tmp.Name(), dest); e != nil {
			return "", e
		}
	}
	return dest, nil
}
func checkInstalledMCP(ctx context.Context, exe, dir string) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "wenshu-setup", Version: Version}, nil)
	session, e := client.Connect(ctx, &mcp.CommandTransport{Command: exec.Command(exe, "--data-dir", dir, "serve")}, nil)
	if e != nil {
		return 0, e
	}
	defer session.Close()
	result, e := session.ListTools(ctx, nil)
	if e != nil {
		return 0, e
	}
	return len(result.Tools), nil
}

func mergeClientConfig(path, format, exe, dir string) (string, error) {
	raw, e := os.ReadFile(path)
	exists := e == nil
	if e != nil && !os.IsNotExist(e) {
		return "", e
	}
	original := map[string]any{}
	if len(bytes.TrimSpace(raw)) > 0 {
		if format == "toml" {
			e = toml.Unmarshal(raw, &original)
		} else {
			e = json.Unmarshal(raw, &original)
		}
		if e != nil {
			return "", fmt.Errorf("原配置格式错误: %w", e)
		}
	}
	key := "mcpServers"
	if format == "toml" {
		key = "mcp_servers"
	}
	servers, ok := original[key].(map[string]any)
	if !ok && original[key] != nil {
		return "", fmt.Errorf("%s 不是配置对象", key)
	}
	if servers == nil {
		servers = map[string]any{}
		original[key] = servers
	}
	entry, ok := servers["wenshu"].(map[string]any)
	if !ok && servers["wenshu"] != nil {
		return "", fmt.Errorf("wenshu 配置不是对象")
	}
	if entry == nil {
		entry = map[string]any{}
	}
	entry["command"] = exe
	entry["args"] = []string{"serve"}
	env, ok := entry["env"].(map[string]any)
	if !ok && entry["env"] != nil {
		return "", fmt.Errorf("wenshu.env 不是对象")
	}
	if env == nil {
		env = map[string]any{}
	}
	env["WENSHU_DATA_DIR"] = dir
	entry["env"] = env
	if format == "toml" {
		entry["enabled"] = true
	}
	servers["wenshu"] = entry
	var output []byte
	if format == "toml" {
		output, e = patchTOMLServer(raw, original, entry)
	} else {
		output, e = json.MarshalIndent(original, "", "  ")
		output = append(output, '\n')
	}
	if e != nil {
		return "", e
	}
	if bytes.Equal(raw, output) {
		return "", nil
	}
	backup := ""
	if exists {
		backup = path + ".wenshu-backup-" + time.Now().Format("20060102-150405.000000000")
		if e = atomicWrite(backup, raw); e != nil {
			return "", e
		}
	}
	if e = atomicWrite(path, output); e != nil {
		return "", e
	}
	return backup, nil
}
func patchTOMLServer(raw []byte, expected, entry map[string]any) ([]byte, error) {
	type span struct{ start, end int }
	cuts := []span{}
	var parser unstable.Parser
	parser.Reset(raw)
	table := []string{}
	for parser.NextExpression() {
		n := parser.Expression()
		if n.Kind != unstable.Table && n.Kind != unstable.ArrayTable && n.Kind != unstable.KeyValue {
			continue
		}
		keys := []string{}
		it := n.Key()
		for it.Next() {
			keys = append(keys, string(it.Node().Data))
		}
		if n.Kind == unstable.Table || n.Kind == unstable.ArrayTable {
			table = keys
			if len(keys) >= 2 && keys[0] == "mcp_servers" && keys[1] == "wenshu" {
				it := n.Key()
				it.Next()
				start := int(it.Node().Raw.Offset)
				for start > 0 && raw[start-1] != '\n' {
					start--
				}
				end := start
				for end < len(raw) && raw[end] != '\n' {
					end++
				}
				cuts = append(cuts, span{start, end})
			}
			continue
		}
		full := append(append([]string{}, table...), keys...)
		if len(full) >= 2 && full[0] == "mcp_servers" && full[1] == "wenshu" {
			cuts = append(cuts, span{int(n.Raw.Offset), int(n.Raw.Offset + n.Raw.Length)})
		}
	}
	if e := parser.Error(); e != nil {
		return nil, e
	}
	var b bytes.Buffer
	pos := 0
	for _, s := range cuts {
		if s.start < pos {
			continue
		}
		b.Write(raw[pos:s.start])
		pos = s.end
	}
	b.Write(raw[pos:])
	block, e := toml.Marshal(map[string]any{"mcp_servers": map[string]any{"wenshu": entry}})
	if e != nil {
		return nil, e
	}
	b.WriteByte('\n')
	b.Write(block)
	candidate := b.Bytes()
	var parsed, normalized map[string]any
	full, e := toml.Marshal(expected)
	if e != nil {
		return nil, e
	}
	if e = toml.Unmarshal(full, &normalized); e != nil {
		return nil, e
	}
	if e = toml.Unmarshal(candidate, &parsed); e == nil && reflect.DeepEqual(parsed, normalized) {
		return candidate, nil
	}
	return full, nil
}
