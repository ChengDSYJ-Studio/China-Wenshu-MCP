package wenshu

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
	"gopkg.in/yaml.v3"
)

func TestAllClientConfigs(t *testing.T) {
	all := clientTargets(t.TempDir(), "darwin", "")
	if len(all) != 27 {
		t.Fatal(len(all))
	}
	seen := map[string]bool{}
	for _, target := range all {
		if seen[target.ID] {
			t.Fatal(target.ID)
		}
		seen[target.ID] = true
		b, e := configSnippet(target, "/程序 空格/wenshu", "/数据 目录")
		if e != nil {
			t.Fatal(target.ID, e)
		}
		switch target.Format {
		case "toml":
			var d map[string]any
			if e = toml.Unmarshal(b, &d); e != nil {
				t.Fatal(e)
			}
		case "dsh":
			var d []map[string]any
			if e = yaml.Unmarshal(b, &d); e != nil || len(d) != 1 {
				t.Fatal(e)
			}
		default:
			var d map[string]any
			if e = json.Unmarshal(b, &d); e != nil {
				t.Fatal(e)
			}
			for _, key := range strings.Split(target.serverKey(), ".") {
				d = d[key].(map[string]any)
			}
			w := d["wenshu"].(map[string]any)
			if w["command"] != "/程序 空格/wenshu" {
				t.Fatal(w)
			}
		}
	}
}
func TestClientImportLinks(t *testing.T) {
	count := 0
	for _, target := range clientTargets(t.TempDir(), "darwin", "") {
		if !target.Link {
			continue
		}
		count++
		u, e := url.Parse(clientImportURL(target, "/程序 空格/wenshu", "/数据"))
		if e != nil {
			t.Fatal(e)
		}
		key := "config"
		if target.ID == "chatbox" {
			key = "server"
		}
		if target.ID == "cherry-studio" {
			key = "servers"
		}
		b, e := base64.StdEncoding.DecodeString(u.Query().Get(key))
		if e != nil {
			t.Fatal(target.ID, e)
		}
		if target.ID == "qoder" {
			s, e := url.QueryUnescape(string(b))
			if e != nil {
				t.Fatal(e)
			}
			b = []byte(s)
		}
		if !json.Valid(b) || !strings.Contains(string(b), "wenshu") {
			t.Fatal(target.ID, string(b))
		}
	}
	if count != 4 {
		t.Fatal(count)
	}
}
func TestNestedClientMerge(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(path, []byte(`{"theme":"dark","mcp":{"other":"keep","servers":{"existing":{"command":"old"}}}}`), 0600)
	_, e := mergeClientTarget(clientTarget{Path: path, Format: "json", Key: "mcp.servers", Extra: map[string]any{"type": "stdio"}}, "exe", "data")
	if e != nil {
		t.Fatal(e)
	}
	b, _ := os.ReadFile(path)
	var d map[string]any
	json.Unmarshal(b, &d)
	m := d["mcp"].(map[string]any)
	if d["theme"] != "dark" || m["other"] != "keep" || len(m["servers"].(map[string]any)) != 2 {
		t.Fatal(d)
	}
}
func TestDSHPreservesOtherPlugins(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cordis.patch.yml")
	before := `# profile
- insert:
    - id: other
      name: example/plugin
      config: {keep: yes}
    - id: mcp-wenshu
      name: '@deepseek-ai/dsh-mcp-client'
      config:
        serverName: wenshu
        command: old
        extra: keep
        env: {EXISTING: retained}
`
	os.WriteFile(path, []byte(before), 0600)
	backup, e := mergeDSH(path, "/new program", "/data")
	if e != nil {
		t.Fatal(e)
	}
	if b, _ := os.ReadFile(backup); string(b) != before {
		t.Fatal("backup")
	}
	b, _ := os.ReadFile(path)
	var ops []map[string]any
	if e = yaml.Unmarshal(b, &ops); e != nil {
		t.Fatal(e)
	}
	items := ops[0]["insert"].([]any)
	if len(items) != 2 {
		t.Fatal(items)
	}
	cfg := items[1].(map[string]any)["config"].(map[string]any)
	if cfg["command"] != "/new program" || cfg["extra"] != "keep" || cfg["env"].(map[string]any)["EXISTING"] != "retained" {
		t.Fatal(cfg)
	}
	if _, e = mergeDSH(path, "/new program", "/data"); e != nil {
		t.Fatal(e)
	}
}
