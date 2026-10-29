package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"wenshu-mcp/internal/wenshu"
)

const help = `wenshu-mcp — 裁判文书网 HTTP 采集与本地研究工具

用法：wenshu-mcp [--data-dir PATH] [--gap 300ms] COMMAND [选项]
不指定 COMMAND 时启动 stdio MCP 服务。全局选项放在 COMMAND 前。

setup                    安装向导：选择客户端、配置浏览器、自动接入
serve                    启动 MCP（stdio）
auth-import --file PATH  导入现有 probe capture / storage state / Cookie
login [--chrome PATH]    打开登录浏览器并保存会话
session-status [--check] 查看会话，--check 实际请求一次
search --query JSON_OR_PATH [--page 1] [--page-size 5]
facets --query JSON_OR_PATH --field s33
dictionary [--key ajlx]  网站查询字典
document --id ID [--fetch] [--force]
task-create --spec PATH  创建任务（样例见 examples/job.json）
task-run --job ID [--max-pages N]  运行/续采，Ctrl+C 保留断点
task-list | task-status --job ID | task-shards --job ID
task-pause --job ID      暂停另一进程中的采集任务
corpus [--job ID] [--keyword TEXT] [--limit 20] [--offset 0]
codebook-import --file PATH
codebook-list
codebook-apply --book NAME --version VERSION [--job ID] [--coder NAME]
coding-put --file PATH   保存人工编码
codings [--job ID] [--book NAME] [--version VERSION] [--coder NAME]
export --out NEW_DIR [--job ID] [--format both|csv|dta]
       [--book NAME --version VERSION --coder NAME]
methods [--job ID] [--out PATH]
version
`

func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
func readJSON(source string, out any) error {
	var data []byte
	var e error
	if strings.HasPrefix(strings.TrimSpace(source), "{") {
		data = []byte(source)
	} else {
		data, e = os.ReadFile(source)
		if e != nil {
			return e
		}
	}
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	return dec.Decode(out)
}
func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
func run() error {
	global := flag.NewFlagSet("wenshu-mcp", flag.ContinueOnError)
	global.SetOutput(os.Stderr)
	data := global.String("data-dir", wenshu.DefaultDataDir(), "会话与语料库目录")
	gap := global.Duration("gap", 300*time.Millisecond, "HTTP 请求间隔")
	global.Usage = func() { fmt.Fprint(os.Stderr, help) }
	if e := global.Parse(os.Args[1:]); e != nil {
		if errors.Is(e, flag.ErrHelp) {
			return nil
		}
		return e
	}
	if *gap < 0 {
		return fmt.Errorf("gap 不能为负")
	}
	args := global.Args()
	cmd := "serve"
	if len(args) > 0 {
		cmd = args[0]
		args = args[1:]
	}
	if cmd == "help" {
		fmt.Print(help)
		return nil
	}
	if cmd == "version" {
		fmt.Println(wenshu.Version)
		return nil
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if cmd == "setup" {
		f := flag.NewFlagSet("setup", flag.ContinueOnError)
		clients := f.String("client", "", "codex/claude-desktop/claude-code/cursor/windsurf/gemini/generic，可用逗号分隔")
		browser := f.String("browser", "", "登录浏览器路径")
		source := f.String("download-source", "", "cn（国内镜像）、official 或自定义镜像根地址")
		configPath := f.String("config-path", "", "客户端配置文件路径")
		if e := f.Parse(args); e != nil {
			return e
		}
		if len(f.Args()) > 0 {
			return fmt.Errorf("无法识别的参数 %v", f.Args())
		}
		return wenshu.RunSetup(ctx, *data, os.Stdin, os.Stdout, wenshu.SetupOptions{Clients: *clients, Browser: *browser, DownloadSource: *source, ConfigPath: *configPath})
	}
	a, e := wenshu.OpenApp(ctx, *data)
	if e != nil {
		return e
	}
	defer a.Close()
	a.SetGap(*gap)
	f := flag.NewFlagSet(cmd, flag.ContinueOnError)
	f.SetOutput(os.Stderr)
	parse := func() error {
		if e := f.Parse(args); e != nil {
			return e
		}
		if len(f.Args()) > 0 {
			return fmt.Errorf("无法识别的参数 %v", f.Args())
		}
		return nil
	}
	var out any
	switch cmd {
	case "serve":
		if e = parse(); e != nil {
			return e
		}
		return a.Server().Run(ctx, &mcp.StdioTransport{})
	case "auth-import":
		file := f.String("file", "", "会话源文件")
		if e = parse(); e != nil {
			return e
		}
		if e = wenshu.ImportSession(*file, a.Client.SessionPath); e != nil {
			return e
		}
		out, e = a.SessionStatus(false)
	case "login":
		chrome := f.String("chrome", "", "Chrome 可执行文件")
		if e = parse(); e != nil {
			return e
		}
		st, e := a.Login.Start(ctx, a.Client.SessionPath, filepath.Join(a.DataDir, "browser-profile"), *chrome)
		if e != nil {
			return e
		}
		fmt.Fprintln(os.Stderr, st.Message)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-ticker.C:
				st = a.Login.Status()
				if st.State == "ready" {
					return printJSON(st)
				}
				if st.State == "error" {
					return fmt.Errorf("%s", st.Message)
				}
			}
		}
	case "session-status":
		check := f.Bool("check", false, "检查网站登录状态")
		if e = parse(); e != nil {
			return e
		}
		out, e = a.SessionStatus(*check)
	case "search", "facets":
		source := f.String("query", "{}", "查询 JSON 或文件路径")
		page := f.Int("page", 1, "页码")
		size := f.Int("page-size", 5, "每页条数")
		field := f.String("field", "s33", "分类字段")
		if e = parse(); e != nil {
			return e
		}
		var q wenshu.Query
		if e = readJSON(*source, &q); e != nil {
			return e
		}
		if cmd == "search" {
			out, e = a.Client.Search(ctx, q, *page, *size)
		} else {
			out, e = a.Client.Facets(ctx, q, *field)
		}
	case "dictionary":
		key := f.String("key", "", "字典键")
		if e = parse(); e != nil {
			return e
		}
		out, e = a.Dictionary(*key)
	case "document":
		id := f.String("id", "", "文书 ID")
		fetch := f.Bool("fetch", false, "从 HTTP 获取正文")
		force := f.Bool("force", false, "重新获取")
		if e = parse(); e != nil {
			return e
		}
		d, readErr := a.Store.Document(*id)
		if *fetch && (readErr != nil || d.BodyStatus == "pending" || *force) {
			remote, err := a.Store.RemoteID(*id)
			if err != nil {
				return err
			}
			raw, err := a.Client.Detail(ctx, remote)
			if err != nil {
				return err
			}
			if err = a.Store.SaveDetail(remote, raw); err != nil {
				return err
			}
			d, readErr = a.Store.Document(*id)
		}
		out, e = d, readErr
	case "task-create":
		spec := f.String("spec", "", "任务 JSON 文件")
		if e = parse(); e != nil {
			return e
		}
		var j wenshu.JobSpec
		if e = readJSON(*spec, &j); e != nil {
			return e
		}
		out, e = a.Store.CreateJob(j)
	case "task-run":
		id := f.String("job", "", "任务 ID")
		maxPages := f.Int("max-pages", 0, "本次最多提交列表页数，0 不限")
		if e = parse(); e != nil {
			return e
		}
		a.Client.Log = func(r wenshu.RequestLog) { a.Store.LogRequest(*id, r) }
		r := wenshu.Runner{Store: a.Store, API: a.Client}
		e = r.Run(ctx, *id, wenshu.RunOptions{MaxPages: *maxPages})
		j, readErr := a.Store.Job(*id)
		if readErr != nil {
			return readErr
		}
		if pe := printJSON(j); pe != nil {
			return pe
		}
		if j.Status == "paused" {
			return nil
		}
		return e
	case "task-list":
		if e = parse(); e != nil {
			return e
		}
		out, e = a.Store.Jobs()
	case "task-status", "task-shards", "task-pause":
		id := f.String("job", "", "任务 ID")
		if e = parse(); e != nil {
			return e
		}
		if cmd == "task-shards" {
			out, e = a.Store.Shards(*id)
		} else {
			if cmd == "task-pause" {
				if e = a.Manager.Pause(*id); e != nil {
					return e
				}
			}
			out, e = a.Store.Job(*id)
		}
	case "corpus":
		id := f.String("job", "", "任务 ID")
		word := f.String("keyword", "", "中文子串")
		limit := f.Int("limit", 20, "页大小")
		offset := f.Int("offset", 0, "偏移")
		if e = parse(); e != nil {
			return e
		}
		out, e = a.Store.Corpus(*id, *word, *limit, *offset)
	case "codebook-import":
		file := f.String("file", "", "JSON/YAML 文件")
		if e = parse(); e != nil {
			return e
		}
		out, e = a.Store.ImportCodebook(*file)
	case "codebook-list":
		if e = parse(); e != nil {
			return e
		}
		out, e = a.Store.Codebooks()
	case "codebook-apply", "codings":
		id := f.String("job", "", "任务 ID")
		book := f.String("book", "", "codebook 名称")
		version := f.String("version", "", "版本")
		coder := f.String("coder", "", "编码员")
		if e = parse(); e != nil {
			return e
		}
		if cmd == "codebook-apply" {
			out, e = a.Store.ApplyCodebook(*id, *book, *version, *coder)
		} else {
			out, e = a.Store.Codings(*id, *book, *version, *coder)
		}
	case "coding-put":
		file := f.String("file", "", "编码 JSON 文件")
		if e = parse(); e != nil {
			return e
		}
		var c wenshu.Coding
		if e = readJSON(*file, &c); e != nil {
			return e
		}
		c.Source = "manual"
		e = a.Store.PutCoding(c)
		out = map[string]any{"saved": e == nil}
	case "export":
		dir := f.String("out", "", "新目录")
		id := f.String("job", "", "任务 ID")
		book := f.String("book", "", "codebook 名称")
		version := f.String("version", "", "版本")
		coder := f.String("coder", "", "编码员")
		format := f.String("format", "both", "both/csv/dta")
		if e = parse(); e != nil {
			return e
		}
		out, e = a.Store.Export(wenshu.ExportOptions{Directory: *dir, JobID: *id, Book: *book, Version: *version, Coder: *coder, Format: *format})
	case "methods":
		id := f.String("job", "", "任务 ID")
		path := f.String("out", "", "输出 Markdown 文件")
		if e = parse(); e != nil {
			return e
		}
		text, err := a.Store.Methods(*id)
		if err != nil {
			return err
		}
		if *path == "" {
			fmt.Print(text)
			return nil
		}
		if e = os.WriteFile(*path, []byte(text), 0600); e != nil {
			return e
		}
		out = map[string]any{"path": *path}
	default:
		return fmt.Errorf("未知命令 %s；运行 wenshu-mcp help", cmd)
	}
	if e != nil {
		return e
	}
	return printJSON(out)
}
