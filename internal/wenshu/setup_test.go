package wenshu

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

func TestSetupPreservesCodexConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	original := `# my settings
model = "my-model"
note = '''
[mcp_servers.wenshu]
this is text
'''
[mcp_servers.other]
command = "keep-me"
[mcp_servers.wenshu]
command = "old"
tool_timeout_sec = 180
[mcp_servers.wenshu.env]
EXISTING = "keep-env"
[projects."/my/project"]
trust_level = "trusted"
`
	if e := os.WriteFile(path, []byte(original), 0600); e != nil {
		t.Fatal(e)
	}
	backup, e := mergeClientConfig(path, "toml", "/有空格的路径/new program", dir)
	if e != nil {
		t.Fatal(e)
	}
	old, e := os.ReadFile(backup)
	if e != nil || string(old) != original {
		t.Fatal("backup did not preserve original", e)
	}
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	var d map[string]any
	if e = toml.Unmarshal(b, &d); e != nil {
		t.Fatal(e)
	}
	if d["model"] != "my-model" || !strings.Contains(string(b), "# my settings") {
		t.Fatal("unrelated settings changed")
	}
	servers := d["mcp_servers"].(map[string]any)
	if servers["other"].(map[string]any)["command"] != "keep-me" {
		t.Fatal("other server changed")
	}
	w := servers["wenshu"].(map[string]any)
	if w["command"] != "/有空格的路径/new program" || w["tool_timeout_sec"] != int64(180) || w["env"].(map[string]any)["EXISTING"] != "keep-env" {
		t.Fatal(w)
	}
	if _, e = mergeClientConfig(path, "toml", "/有空格的路径/new program", dir); e != nil {
		t.Fatal("reinstall", e)
	}
}
func TestSetupJSONMergeAndMalformedConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	os.WriteFile(path, []byte(`{"theme":"dark","mcpServers":{"other":{"command":"old"}}}`), 0600)
	if _, e := mergeClientConfig(path, "json", `C:\Program Files\Wenshu\wenshu-mcp.exe`, dir); e != nil {
		t.Fatal(e)
	}
	b, _ := os.ReadFile(path)
	var d map[string]any
	if e := json.Unmarshal(b, &d); e != nil {
		t.Fatal(e)
	}
	if d["theme"] != "dark" || len(d["mcpServers"].(map[string]any)) != 2 {
		t.Fatal(d)
	}
	broken := []byte(`{"theme":`)
	os.WriteFile(path, broken, 0600)
	if _, e := mergeClientConfig(path, "json", "exe", dir); e == nil {
		t.Fatal("malformed config accepted")
	}
	got, _ := os.ReadFile(path)
	if !bytes.Equal(got, broken) {
		t.Fatal("malformed config overwritten")
	}
}
func TestSetupDownloadSourcesAndPlatforms(t *testing.T) {
	for _, target := range [][2]string{{"darwin", "arm64"}, {"darwin", "amd64"}, {"windows", "amd64"}, {"windows", "arm64"}, {"linux", "amd64"}, {"linux", "arm64"}} {
		platform, e := browserPlatform(target[0], target[1])
		if e != nil {
			t.Fatal(e)
		}
		urls := browserURLs(platform, "cn")
		if len(urls) != 3 || !strings.Contains(urls[0], "npmmirror.com") || !strings.Contains(urls[2], "storage.googleapis.com") {
			t.Fatal(urls)
		}
		if len(browserURLs(platform, "official")) != 1 {
			t.Fatal("official source")
		}
		if !strings.HasPrefix(browserURLs(platform, "https://mirror.example/browser")[0], "https://mirror.example/browser/") {
			t.Fatal("custom mirror")
		}
	}
}
func TestSetupExtractBrowser(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "a.zip")
	f, _ := os.Create(archive)
	z := zip.NewWriter(f)
	h := &zip.FileHeader{Name: "chrome-test/chrome", Method: zip.Deflate}
	h.SetMode(0755)
	w, _ := z.CreateHeader(h)
	w.Write([]byte("executable"))
	z.Close()
	f.Close()
	if e := extractBrowser(archive, filepath.Join(dir, "out")); e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile(filepath.Join(dir, "out", "chrome-test", "chrome"))
	if e != nil || string(b) != "executable" {
		t.Fatal(e)
	}
	f, _ = os.Create(archive)
	z = zip.NewWriter(f)
	w, _ = z.Create("../outside")
	w.Write([]byte("bad"))
	z.Close()
	f.Close()
	if e = extractBrowser(archive, filepath.Join(dir, "other")); e == nil {
		t.Fatal("unsafe archive path accepted")
	}
}
