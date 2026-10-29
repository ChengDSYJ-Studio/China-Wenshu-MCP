package wenshu

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const bundledChromeVersion = "156.0.8078.73"

type BrowserChoice struct{ Name, Path string }

func normalizeBrowser(path string) (string, error) {
	path = strings.Trim(strings.TrimSpace(path), "\"'")
	if strings.HasSuffix(strings.ToLower(path), ".app") {
		path = filepath.Join(path, "Contents", "MacOS", strings.TrimSuffix(filepath.Base(path), ".app"))
	}
	if p, e := exec.LookPath(path); e == nil {
		path = p
	}
	abs, e := filepath.Abs(path)
	if e != nil {
		return "", e
	}
	info, e := os.Stat(abs)
	if e != nil {
		return "", e
	}
	if info.IsDir() {
		return "", fmt.Errorf("请选择浏览器可执行文件：%s", abs)
	}
	return abs, nil
}
func DetectBrowsers(dir string) []BrowserChoice {
	home, _ := os.UserHomeDir()
	candidates := []BrowserChoice{}
	add := func(name, path string) {
		if path != "" {
			candidates = append(candidates, BrowserChoice{name, path})
		}
	}
	if s, e := loadSettings(dir); e == nil {
		add("已配置浏览器", s.BrowserPath)
	}
	if runtime.GOOS == "darwin" {
		for _, base := range []string{"/Applications", filepath.Join(home, "Applications")} {
			for _, name := range []string{"Google Chrome", "Microsoft Edge", "Chromium", "Brave Browser", "Google Chrome for Testing"} {
				add(name, filepath.Join(base, name+".app", "Contents", "MacOS", name))
			}
		}
		add("Chrome for Testing", filepath.Join(home, "Library", "Application Support", "CNKI-MCP", "runtime", "Google Chrome for Testing.app", "Contents", "MacOS", "Google Chrome for Testing"))
	}
	if runtime.GOOS == "windows" {
		for _, base := range []string{os.Getenv("PROGRAMFILES"), os.Getenv("PROGRAMFILES(X86)"), os.Getenv("LOCALAPPDATA")} {
			if base == "" {
				continue
			}
			add("Google Chrome", filepath.Join(base, "Google", "Chrome", "Application", "chrome.exe"))
			add("Microsoft Edge", filepath.Join(base, "Microsoft", "Edge", "Application", "msedge.exe"))
			add("Brave", filepath.Join(base, "BraveSoftware", "Brave-Browser", "Application", "brave.exe"))
			add("Chromium", filepath.Join(base, "Chromium", "Application", "chrome.exe"))
		}
	}
	for _, name := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "microsoft-edge", "microsoft-edge-stable", "brave-browser", "chrome.exe", "msedge.exe", "brave.exe"} {
		if path, e := exec.LookPath(name); e == nil {
			add(name, path)
		}
	}
	out := []BrowserChoice{}
	seen := map[string]bool{}
	for _, c := range candidates {
		if p, e := normalizeBrowser(c.Path); e == nil && !seen[p] {
			seen[p] = true
			c.Path = p
			out = append(out, c)
		}
	}
	return out
}
func ResolveBrowser(explicit, dir string) (string, error) {
	if explicit != "" {
		return normalizeBrowser(explicit)
	}
	if path := os.Getenv("WENSHU_BROWSER_PATH"); path != "" {
		return normalizeBrowser(path)
	}
	choices := DetectBrowsers(dir)
	if len(choices) > 0 {
		return choices[0].Path, nil
	}
	return "", fmt.Errorf("请运行安装设置以配置登录浏览器")
}
func browserPlatform(goos, arch string) (string, error) {
	switch goos + "/" + arch {
	case "darwin/arm64":
		return "mac-arm64", nil
	case "darwin/amd64":
		return "mac-x64", nil
	case "windows/amd64", "windows/arm64":
		return "win64", nil
	case "linux/amd64":
		return "linux64", nil
	case "linux/arm64":
		return "linux-arm64", nil
	}
	return "", fmt.Errorf("当前平台没有配套浏览器：%s/%s", goos, arch)
}
func browserURLs(platform, source string) []string {
	file := bundledChromeVersion + "/" + platform + "/chrome-" + platform + ".zip"
	official := "https://storage.googleapis.com/chrome-for-testing-public/" + file
	switch source {
	case "official":
		return []string{official}
	case "", "cn", "auto":
		return []string{"https://registry.npmmirror.com/-/binary/chrome-for-testing/" + file, "https://cdn.npmmirror.com/binaries/chrome-for-testing/" + file, official}
	default:
		return []string{strings.TrimRight(source, "/") + "/" + file}
	}
}
func browserExecutable(root, platform string) string {
	base := filepath.Join(root, "chrome-"+platform)
	switch {
	case strings.HasPrefix(platform, "mac-"):
		return filepath.Join(base, "Google Chrome for Testing.app", "Contents", "MacOS", "Google Chrome for Testing")
	case strings.HasPrefix(platform, "win"):
		return filepath.Join(base, "chrome.exe")
	default:
		return filepath.Join(base, "chrome")
	}
}
func DownloadBrowser(ctx context.Context, dir, source string, out io.Writer) (string, error) {
	platform, e := browserPlatform(runtime.GOOS, runtime.GOARCH)
	if e != nil {
		return "", e
	}
	dest := filepath.Join(dir, "runtime", "chrome-"+bundledChromeVersion)
	exe := browserExecutable(dest, platform)
	if _, e = os.Stat(exe); e == nil {
		return exe, nil
	}
	if e = os.MkdirAll(filepath.Dir(dest), 0700); e != nil {
		return "", e
	}
	stage, e := os.MkdirTemp(filepath.Dir(dest), ".chrome-install-")
	if e != nil {
		return "", e
	}
	defer os.RemoveAll(stage)
	archive := filepath.Join(stage, "browser.zip")
	client := &http.Client{Timeout: 15 * time.Minute, Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, DialContext: (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext, ResponseHeaderTimeout: 20 * time.Second, TLSHandshakeTimeout: 15 * time.Second}}
	var last error
	downloaded := false
	for _, url := range browserURLs(platform, source) {
		fmt.Fprintf(out, "正在下载配套浏览器：%s\n", url)
		if e = downloadFile(ctx, client, url, archive, out); e == nil {
			downloaded = true
			break
		}
		last = e
		fmt.Fprintf(out, "此下载源暂不可用，继续尝试备用源：%v\n", e)
	}
	if !downloaded {
		return "", fmt.Errorf("浏览器下载失败：%w", last)
	}
	extract := filepath.Join(stage, "files")
	if e = extractBrowser(archive, extract); e != nil {
		return "", e
	}
	if _, e = os.Stat(browserExecutable(extract, platform)); e != nil {
		return "", fmt.Errorf("下载包中缺少浏览器程序: %w", e)
	}
	if _, e = os.Stat(dest); e == nil {
		dest = dest + fmt.Sprintf("-%d", time.Now().Unix())
		exe = browserExecutable(dest, platform)
	}
	if e = os.Rename(extract, dest); e != nil {
		return "", e
	}
	fmt.Fprintln(out, "配套浏览器已安装。")
	return exe, nil
}

type downloadProgress struct {
	out     io.Writer
	written int64
	last    time.Time
}

func (p *downloadProgress) Write(b []byte) (int, error) {
	p.written += int64(len(b))
	if time.Since(p.last) > time.Second {
		fmt.Fprintf(p.out, "\r已下载 %.1f MB", float64(p.written)/(1024*1024))
		p.last = time.Now()
	}
	return len(b), nil
}
func downloadFile(ctx context.Context, client *http.Client, url, path string, out io.Writer) error {
	req, e := http.NewRequestWithContext(ctx, "GET", url, nil)
	if e != nil {
		return e
	}
	res, e := client.Do(req)
	if e != nil {
		return e
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("HTTP %d", res.StatusCode)
	}
	f, e := os.Create(path)
	if e != nil {
		return e
	}
	_, copyErr := io.Copy(io.MultiWriter(f, &downloadProgress{out: out}), res.Body)
	closeErr := f.Close()
	fmt.Fprintln(out)
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}
func extractBrowser(archive, dest string) error {
	r, e := zip.OpenReader(archive)
	if e != nil {
		return e
	}
	defer r.Close()
	if e = os.MkdirAll(dest, 0700); e != nil {
		return e
	}
	for _, f := range r.File {
		rel := filepath.FromSlash(f.Name)
		if filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(filepath.Clean(rel), ".."+string(filepath.Separator)) {
			return fmt.Errorf("压缩包路径错误")
		}
		path := filepath.Join(dest, rel)
		if f.FileInfo().IsDir() {
			if e = os.MkdirAll(path, 0755); e != nil {
				return e
			}
			continue
		}
		if e = os.MkdirAll(filepath.Dir(path), 0755); e != nil {
			return e
		}
		in, e := f.Open()
		if e != nil {
			return e
		}
		if f.Mode()&os.ModeSymlink != 0 {
			b, e := io.ReadAll(io.LimitReader(in, 4096))
			in.Close()
			if e != nil {
				return e
			}
			target := string(b)
			inside, e := filepath.Rel(dest, filepath.Join(filepath.Dir(path), target))
			if e != nil || filepath.IsAbs(target) || inside == ".." || strings.HasPrefix(inside, ".."+string(filepath.Separator)) {
				return fmt.Errorf("压缩包链接路径错误")
			}
			if e = os.Symlink(target, path); e != nil {
				return e
			}
			continue
		}
		mode := f.Mode().Perm()
		if mode == 0 {
			mode = 0644
		}
		out, e := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
		if e != nil {
			in.Close()
			return e
		}
		_, copyErr := io.Copy(out, in)
		in.Close()
		closeErr := out.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}
