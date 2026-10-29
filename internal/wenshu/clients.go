package wenshu

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
	"gopkg.in/yaml.v3"
)

type clientTarget struct {
	ID, Name, Path, Format string
	Key                    string
	Extra                  map[string]any
	ImportHint             string
	Link                   bool
	NeedDir                bool
}

func clientTargets(home, goos, appData string) []clientTarget {
	envPath := func(key, fallback string) string {
		if v := os.Getenv(key); v != "" {
			return v
		}
		return fallback
	}
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
	dsh := envPath("DSH_HOME", filepath.Join(home, ".dsh"))
	return []clientTarget{
		{ID: "codex", Name: "Codex", Path: filepath.Join(envPath("CODEX_HOME", filepath.Join(home, ".codex")), "config.toml"), Format: "toml"},
		{ID: "claude-desktop", Name: "Claude Desktop", Path: claude, Format: "json"},
		{ID: "claude-code", Name: "Claude Code", Path: filepath.Join(envPath("CLAUDE_CONFIG_DIR", home), ".claude.json"), Format: "json", Extra: map[string]any{"type": "stdio"}},
		{ID: "cursor", Name: "Cursor", Path: filepath.Join(home, ".cursor", "mcp.json"), Format: "json", Extra: map[string]any{"type": "stdio"}},
		{ID: "gemini", Name: "Gemini CLI", Path: filepath.Join(home, ".gemini", "settings.json"), Format: "json", Extra: map[string]any{"timeout": 900000}},
		{ID: "dsh-desktop", Name: "DeepSeek Harness 桌面版", Path: filepath.Join(dsh, "profiles", "desktop", "cordis.patch.yml"), Format: "dsh", NeedDir: true, ImportHint: "运行一次 DeepSeek Harness 桌面版后可自动写入，也可将此 YAML 导入对应 profile"},
		{ID: "dsh-web", Name: "DeepSeek Harness 网页版", Path: filepath.Join(dsh, "profiles", "web", "cordis.patch.yml"), Format: "dsh", NeedDir: true, ImportHint: "运行一次 dsh web 后可自动写入，也可将此 YAML 导入对应 profile"},
		{ID: "workbuddy", Name: "WorkBuddy", Path: filepath.Join(home, ".workbuddy", "mcp.json"), Format: "json"},
		{ID: "zcode", Name: "ZCode", Path: filepath.Join(home, ".zcode", "cli", "config.json"), Format: "json", Key: "mcp.servers", Extra: map[string]any{"type": "stdio"}},
		{ID: "kimi-code", Name: "Kimi Code", Path: filepath.Join(envPath("KIMI_CODE_HOME", filepath.Join(home, ".kimi-code")), "mcp.json"), Format: "json"},
		{ID: "codebuddy", Name: "CodeBuddy CLI", Path: filepath.Join(home, ".codebuddy", ".mcp.json"), Format: "json", Extra: map[string]any{"type": "stdio"}},
		{ID: "qwen-code", Name: "Qwen Code", Path: filepath.Join(home, ".qwen", "settings.json"), Format: "json", Extra: map[string]any{"timeout": 900000}},
		{ID: "qoder-cli", Name: "Qoder CLI", Path: filepath.Join(home, ".qoder", "settings.json"), Format: "json", Extra: map[string]any{"type": "stdio"}},
		{ID: "qoder", Name: "Qoder IDE", Format: "json", Link: true, ImportHint: "Qoder 设置 → MCP → My Servers → Add"},
		{ID: "trae", Name: "Trae", Format: "json", Link: true, ImportHint: "Trae 设置 → MCP → 添加"},
		{ID: "trae-cn", Name: "Trae CN", Format: "json", ImportHint: "Trae CN 设置 → MCP → 手动添加"},
		{ID: "lingma", Name: "通义灵码", Format: "json", ImportHint: "个人设置 → MCP 服务 → 配置文件添加"},
		{ID: "codebuddy-ide", Name: "CodeBuddy IDE", Format: "json", Extra: map[string]any{"type": "stdio"}, ImportHint: "CodeBuddy 设置 → MCP → Add MCP"},
		{ID: "comate", Name: "文心快码 Comate", Format: "json", Extra: map[string]any{"type": "stdio"}, ImportHint: "Comate 的 MCP 设置 → 导入 mcp.json"},
		{ID: "chatbox", Name: "Chatbox", Format: "json", Link: true, ImportHint: "Chatbox 设置 → MCP → 添加服务器"},
		{ID: "cherry-studio", Name: "Cherry Studio", Format: "json", Link: true, ImportHint: "Cherry Studio 设置 → MCP 服务器 → 从 JSON 导入"},
		{ID: "vscode", Name: "VS Code / GitHub Copilot", Format: "json", Key: "servers", Extra: map[string]any{"type": "stdio"}, ImportHint: "命令面板 → MCP: Open User Configuration"},
		{ID: "windsurf", Name: "Windsurf", Path: filepath.Join(home, ".codeium", "windsurf", "mcp_config.json"), Format: "json", ImportHint: "Windsurf → MCP 配置"},
		{ID: "cline", Name: "Cline", Format: "json", Extra: map[string]any{"timeout": 900, "disabled": false}, ImportHint: "MCP Servers → Configure"},
		{ID: "roo", Name: "Roo Code", Format: "json", Extra: map[string]any{"timeout": 900, "disabled": false}, ImportHint: "MCP 设置 → Edit Global MCP"},
		{ID: "lobehub", Name: "LobeHub / LobeChat", Format: "json", ImportHint: "插件设置 → 自定义 MCP → 导入 JSON"},
		{ID: "generic", Name: "通用 MCP 客户端", Format: "json", ImportHint: "在客户端的 MCP 设置中导入配置文件"},
	}
}
func (t clientTarget) serverKey() string {
	if t.Key != "" {
		return t.Key
	}
	return "mcpServers"
}
func (t clientTarget) extension() string {
	if t.Format == "dsh" {
		return "yaml"
	}
	return t.Format
}
func clientEntry(t clientTarget, exe, dir string) map[string]any {
	m := map[string]any{"command": exe, "args": []string{"serve"}, "env": map[string]string{"WENSHU_DATA_DIR": dir}}
	for k, v := range t.Extra {
		m[k] = v
	}
	return m
}
func wrapClient(t clientTarget, entry map[string]any) map[string]any {
	var obj any = map[string]any{"wenshu": entry}
	parts := strings.Split(t.serverKey(), ".")
	for i := len(parts) - 1; i >= 0; i-- {
		obj = map[string]any{parts[i]: obj}
	}
	return obj.(map[string]any)
}
func dshPlugin(exe, dir string) map[string]any {
	return map[string]any{"id": "mcp-wenshu", "name": "@deepseek-ai/dsh-mcp-client", "config": map[string]any{"serverName": "wenshu", "transport": "stdio", "command": exe, "args": []string{"serve"}, "env": map[string]string{"WENSHU_DATA_DIR": dir}, "toolCallTimeoutMs": 900000, "failOnStartupError": false}}
}
func configSnippet(t clientTarget, exe, dir string) ([]byte, error) {
	if t.Format == "toml" {
		return toml.Marshal(map[string]any{"mcp_servers": map[string]any{"wenshu": map[string]any{"command": exe, "args": []string{"serve"}, "env": map[string]string{"WENSHU_DATA_DIR": dir}, "startup_timeout_sec": 30, "tool_timeout_sec": 900}}})
	}
	if t.Format == "dsh" {
		return yaml.Marshal([]any{map[string]any{"insert": []any{dshPlugin(exe, dir)}}})
	}
	b, e := json.MarshalIndent(wrapClient(t, clientEntry(t, exe, dir)), "", "  ")
	return append(b, '\n'), e
}
func ClientConfig(id, exe, dir string) ([]byte, error) {
	home, e := os.UserHomeDir()
	if e != nil {
		return nil, e
	}
	for _, t := range clientTargets(home, runtime.GOOS, os.Getenv("APPDATA")) {
		if t.ID == id {
			return configSnippet(t, exe, dir)
		}
	}
	return nil, fmt.Errorf("未知客户端 %q", id)
}
func clientImportURL(t clientTarget, exe, dir string) string {
	entry := clientEntry(t, exe, dir)
	encode := func(v any) string { b, _ := json.Marshal(v); return base64.StdEncoding.EncodeToString(b) }
	var u url.URL
	values := url.Values{}
	switch t.ID {
	case "qoder":
		u = url.URL{Scheme: "qoder", Host: "aicoding.aicoding-deeplink", Path: "/mcp/add"}
		b, _ := json.Marshal(entry)
		values.Set("name", "wenshu")
		values.Set("config", base64.StdEncoding.EncodeToString([]byte(url.QueryEscape(string(b)))))
	case "trae":
		u = url.URL{Scheme: "trae", Host: "trae.ai-ide", Path: "/mcp-import"}
		values.Set("name", "wenshu")
		values.Set("type", "stdio")
		values.Set("config", encode(entry))
	case "chatbox":
		u = url.URL{Scheme: "chatbox", Host: "mcp", Path: "/install"}
		entry["name"] = "wenshu"
		values.Set("server", encode(entry))
	case "cherry-studio":
		u = url.URL{Scheme: "cherrystudio", Host: "mcp", Path: "/install"}
		values.Set("servers", encode(wrapClient(t, entry)))
	default:
		return ""
	}
	u.RawQuery = values.Encode()
	return u.String()
}
func openClientImport(ctx context.Context, uri string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.CommandContext(ctx, "open", uri)
	case "windows":
		cmd = exec.CommandContext(ctx, "rundll32", "url.dll,FileProtocolHandler", uri)
	default:
		cmd = exec.CommandContext(ctx, "xdg-open", uri)
	}
	return cmd.Run()
}

func yamlField(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}
func yamlSet(n *yaml.Node, key string, v *yaml.Node) error {
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("DSH 配置应为映射结构")
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			n.Content[i+1] = v
			return nil
		}
	}
	n.Content = append(n.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, v)
	return nil
}
func mergeDSH(path, exe, dir string) (string, error) {
	before, e := os.ReadFile(path)
	exists := e == nil
	if e != nil && !os.IsNotExist(e) {
		return "", e
	}
	var document yaml.Node
	if len(strings.TrimSpace(string(before))) > 0 {
		if e = yaml.Unmarshal(before, &document); e != nil {
			return "", e
		}
	}
	if len(document.Content) == 0 {
		document = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.SequenceNode, Tag: "!!seq"}}}
	}
	root := document.Content[0]
	if root.Kind != yaml.SequenceNode {
		return "", fmt.Errorf("DSH patch 配置应为操作列表")
	}
	var fresh yaml.Node
	if e = fresh.Encode(dshPlugin(exe, dir)); e != nil {
		return "", e
	}
	found := false
	for _, operation := range root.Content {
		items := yamlField(operation, "insert")
		if items == nil {
			continue
		}
		if items.Kind != yaml.SequenceNode {
			return "", fmt.Errorf("DSH insert 应为列表")
		}
		for _, plugin := range items.Content {
			id := yamlField(plugin, "id")
			config := yamlField(plugin, "config")
			name := yamlField(config, "serverName")
			if (id != nil && id.Value == "mcp-wenshu") || (name != nil && name.Value == "wenshu") {
				if found {
					return "", fmt.Errorf("DSH 配置存在多个 wenshu 项，请在客户端选择保留项")
				}
				found = true
				if config == nil {
					config = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
					if e = yamlSet(plugin, "config", config); e != nil {
						return "", e
					}
				}
				fc := yamlField(&fresh, "config")
				for i := 0; i < len(fc.Content); i += 2 {
					key := fc.Content[i].Value
					if key == "env" {
						old := yamlField(config, "env")
						if old != nil {
							if e = yamlSet(old, "WENSHU_DATA_DIR", yamlField(fc.Content[i+1], "WENSHU_DATA_DIR")); e != nil {
								return "", e
							}
							continue
						}
					}
					if e = yamlSet(config, key, fc.Content[i+1]); e != nil {
						return "", e
					}
				}
			}
		}
	}
	if !found {
		var op yaml.Node
		if e = op.Encode(map[string]any{"insert": []any{dshPlugin(exe, dir)}}); e != nil {
			return "", e
		}
		root.Content = append(root.Content, &op)
	}
	after, e := yaml.Marshal(&document)
	if e != nil {
		return "", e
	}
	if string(after) == string(before) {
		return "", nil
	}
	backup := ""
	if exists {
		backup = path + ".wenshu-backup-" + time.Now().Format("20060102-150405.000000000")
		if e = atomicWrite(backup, before); e != nil {
			return "", e
		}
	}
	if e = atomicWrite(path, after); e != nil {
		return "", e
	}
	return backup, nil
}
