package wenshu

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

//go:embed dictionary.json
var dictionary []byte

type App struct {
	Store   *Store
	Manager *Manager
	Login   *LoginManager
	DataDir string
	Client  *Client
	ctx     context.Context
}

func OpenApp(ctx context.Context, dir string) (*App, error) {
	abs, e := filepath.Abs(dir)
	if e != nil {
		return nil, e
	}
	s, e := OpenStore(filepath.Join(abs, "corpus.sqlite"))
	if e != nil {
		return nil, e
	}
	session := filepath.Join(abs, "session.json")
	c := NewClient(session)
	return &App{Store: s, Manager: &Manager{Store: s, SessionPath: session, Gap: 300 * time.Millisecond}, Login: &LoginManager{}, DataDir: abs, Client: c, ctx: ctx}, nil
}
func (a *App) Close() error { a.Login.Close(); a.Manager.Close(); return a.Store.Close() }
func (a *App) SessionStatus(check bool) (map[string]any, error) {
	s, e := readSession(a.Client.SessionPath)
	out := map[string]any{"configured": e == nil, "login": a.Login.Status(), "session_path": a.Client.SessionPath, "version": Version}
	if e != nil {
		return out, nil
	}
	out["imported_at"] = s.ImportedAt
	if check {
		e = a.Client.CheckSession(a.ctx)
		out["checked_via"] = "currentUser"
		out["usable"] = e == nil
		if e != nil {
			out["message"] = e.Error()
		}
	}
	return out, nil
}
func (a *App) Dictionary(key string) (any, error) {
	var d map[string]any
	if e := json.Unmarshal(dictionary, &d); e != nil {
		return nil, e
	}
	if key != "" {
		v, ok := d[key]
		if !ok {
			return nil, fmt.Errorf("字典键不存在 %s", key)
		}
		return v, nil
	}
	keys := []string{}
	for k := range d {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return map[string]any{"keys": keys, "fields": map[string]string{"s21": "全文", "s2": "法院名称", "s4": "法院层级", "s6": "文书类型", "s8": "案件类型", "s9": "审判程序", "s11": "案由", "s33": "法院省份", "s39": "法院", "s40": "下级法院", "s42": "裁判年份", "cprq": "裁判日期范围"}}, nil
}
func addTool[I any](srv *mcp.Server, name, desc string, fn func(context.Context, I) (any, error)) {
	mcp.AddTool(srv, &mcp.Tool{Name: name, Description: desc}, func(ctx context.Context, _ *mcp.CallToolRequest, in I) (*mcp.CallToolResult, any, error) {
		out, e := fn(ctx, in)
		if e != nil {
			return nil, nil, e
		}
		return nil, out, nil
	})
}

type emptyInput struct{}
type jobInput struct {
	JobID string `json:"job_id"`
}
type pathInput struct {
	Path string `json:"path"`
}
type codeFilter struct {
	Limit   int    `json:"limit,omitempty"`
	Offset  int    `json:"offset,omitempty"`
	JobID   string `json:"job_id,omitempty"`
	Book    string `json:"book,omitempty"`
	Version string `json:"version,omitempty"`
	Coder   string `json:"coder,omitempty"`
}

func (a *App) Server() *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "wenshu-mcp", Version: Version}, nil)
	addTool(s, "wenshu_session_status", "查看会话配置、保存时间和版本；check=true 在线检查登录状态。", func(ctx context.Context, in struct {
		Check bool `json:"check,omitempty"`
	}) (any, error) {
		return a.SessionStatus(in.Check)
	})
	addTool(s, "wenshu_auth_import", "导入本地 capture.json、storage_state.json、session.json 或 Cookie 文本。", func(ctx context.Context, in pathInput) (any, error) {
		e := ImportSession(in.Path, a.Client.SessionPath)
		if e != nil {
			return nil, e
		}
		return a.SessionStatus(false)
	})
	addTool(s, "wenshu_login", "打开登录浏览器，登录成功后保存会话并关闭窗口。工具立即返回，可通过会话状态查看结果。", func(ctx context.Context, in struct {
		Chrome string `json:"chrome,omitempty"`
	}) (any, error) {
		return a.Login.Start(a.ctx, a.Client.SessionPath, filepath.Join(a.DataDir, "browser-profile"), in.Chrome)
	})
	addTool(s, "wenshu_search", "HTTP 检索一页；每页最多15条，超过600条请创建自动拆分采集任务。", func(ctx context.Context, in struct {
		Query    Query `json:"query"`
		Page     int   `json:"page,omitempty"`
		PageSize int   `json:"page_size,omitempty"`
	}) (any, error) {
		if in.Page == 0 {
			in.Page = 1
		}
		if in.PageSize == 0 {
			in.PageSize = 5
		}
		return a.Client.Search(ctx, in.Query, in.Page, in.PageSize)
	})
	addTool(s, "wenshu_facets", "查询互斥分类计数，例如 s42 年份、s33 省份、s8 案件类型。", func(ctx context.Context, in struct {
		Query Query  `json:"query"`
		Field string `json:"field"`
	}) (any, error) {
		return a.Client.Facets(ctx, in.Query, in.Field)
	})
	addTool(s, "wenshu_dictionary", "查询案件类型、文书类型、案由等网站字典；不传 key 返回可用字典名。", func(ctx context.Context, in struct {
		Key string `json:"key,omitempty"`
	}) (any, error) {
		return a.Dictionary(in.Key)
	})
	addTool(s, "wenshu_document", "读取本地文书和段落；fetch=true 从 HTTP 获取并入库；force=true 刷新已存正文。", func(ctx context.Context, in struct {
		DocID string `json:"doc_id"`
		Fetch bool   `json:"fetch,omitempty"`
		Force bool   `json:"force,omitempty"`
	}) (any, error) {
		d, e := a.Store.Document(in.DocID)
		if in.Fetch && (e != nil || d.BodyStatus == "pending" || in.Force) {
			remote, e := a.Store.RemoteID(in.DocID)
			if e != nil {
				return nil, e
			}
			raw, e := a.Client.Detail(ctx, remote)
			if e != nil {
				return nil, e
			}
			if e = a.Store.SaveDetail(remote, raw); e != nil {
				return nil, e
			}
			return a.Store.Document(in.DocID)
		}
		return d, e
	})
	addTool(s, "wenshu_task_create", "创建持久化任务；fetch_body=true 同时采正文。创建后用 task_start 执行。", func(ctx context.Context, in JobSpec) (any, error) { return a.Store.CreateJob(in) })
	addTool(s, "wenshu_task_start", "后台启动或从断点恢复任务，立即返回；用 task_status 查进度。", func(ctx context.Context, in struct {
		JobID             string `json:"job_id"`
		MaxPages          int    `json:"max_pages,omitempty"`
		RequestIntervalMS *int   `json:"request_interval_ms,omitempty"`
	}) (any, error) {
		j, e := a.Manager.Start(a.ctx, in.JobID, RunOptions{MaxPages: in.MaxPages, RequestIntervalMS: in.RequestIntervalMS})
		return map[string]any{"accepted": e == nil, "job": j, "note": "使用 wenshu_task_status 读取运行结果"}, e
	})
	addTool(s, "wenshu_task_pause", "暂停任务并保留分页和正文断点。", func(ctx context.Context, in jobInput) (any, error) {
		if e := a.Manager.Pause(in.JobID); e != nil {
			return nil, e
		}
		return a.Store.Job(in.JobID)
	})
	addTool(s, "wenshu_task_status", "读取任务状态、文书数、正文数和各分片状态计数。", func(ctx context.Context, in jobInput) (any, error) { return a.Store.Job(in.JobID) })
	addTool(s, "wenshu_task_list", "列出本地采集任务。", func(ctx context.Context, in emptyInput) (any, error) { return a.Store.Jobs() })
	addTool(s, "wenshu_task_shards", "查看分片条件、接口计数、下一页和无法完成的原因。", func(ctx context.Context, in jobInput) (any, error) { return a.Store.Shards(in.JobID) })
	addTool(s, "wenshu_corpus_search", "在本地语料检索文书；中文子串检索，分页返回元数据。", func(ctx context.Context, in struct {
		JobID   string `json:"job_id,omitempty"`
		Keyword string `json:"keyword,omitempty"`
		Limit   int    `json:"limit,omitempty"`
		Offset  int    `json:"offset,omitempty"`
	}) (any, error) {
		if in.Limit == 0 {
			in.Limit = 20
		}
		return a.Store.Corpus(in.JobID, in.Keyword, in.Limit, in.Offset)
	})
	addTool(s, "wenshu_link_references", "为文本中的《文书标题》和案号添加 Markdown 来源链接。优先精确匹配本地语料；lookup_missing=true 可联网定位文书。返回匹配结果、候选文书和链接。", func(ctx context.Context, in LinkOptions) (any, error) { return a.LinkReferences(ctx, in) })
	addTool(s, "wenshu_codebook_import", "导入 JSON/YAML codebook；同名版本不可覆盖不同规则。", func(ctx context.Context, in pathInput) (any, error) { return a.Store.ImportCodebook(in.Path) })
	addTool(s, "wenshu_codebook_list", "列出已安装 codebook 及其版本与变量定义。", func(ctx context.Context, in emptyInput) (any, error) { return a.Store.Codebooks() })
	addTool(s, "wenshu_code_apply", "按 codebook 正则规则编码本地正文，返回冲突；不命中且无 default 时保留缺失。", func(ctx context.Context, in codeFilter) (any, error) {
		return a.Store.ApplyCodebook(in.JobID, in.Book, in.Version, in.Coder)
	})
	addTool(s, "wenshu_code_put", "保存人工编码。value 为字符串时，按 codebook 转换：布尔使用 true/false，数值使用 JSON 数字，文本原样保存。unit=0 表示文书，正整数表示段落；提供原文证据或说明。", func(ctx context.Context, in Coding) (any, error) {
		in.Source = "manual"
		e := a.Store.PutCoding(in)
		return map[string]any{"saved": e == nil}, e
	})
	addTool(s, "wenshu_codings", "查询编码值、编码员、证据段落与待复核状态。", func(ctx context.Context, in codeFilter) (any, error) {
		if in.Limit == 0 {
			in.Limit = 100
		}
		if in.Limit < 1 || in.Limit > 10000 || in.Offset < 0 {
			return nil, fmt.Errorf("limit 必须为 1..10000，offset 必须非负")
		}
		return a.Store.CodingPage(in.JobID, in.Book, in.Version, in.Coder, in.Limit, in.Offset)
	})
	addTool(s, "wenshu_export", "导出一致快照到新目录：CSV、Stata118、原文JSONL、编码和方法附录。format=both/csv/dta。", func(ctx context.Context, in ExportOptions) (any, error) { return a.Store.Export(in) })
	addTool(s, "wenshu_methods", "生成包含检索条件、拆分与完成情况、局限性的研究方法附录。", func(ctx context.Context, in struct {
		JobID string `json:"job_id,omitempty"`
		Path  string `json:"path,omitempty"`
	}) (any, error) {
		text, e := a.Store.Methods(in.JobID)
		if e != nil {
			return nil, e
		}
		if in.Path != "" {
			if e = atomicWrite(in.Path, []byte(text)); e != nil {
				return nil, e
			}
			return map[string]any{"path": in.Path}, nil
		}
		return map[string]any{"markdown": text}, nil
	})
	return s
}
func DefaultDataDir() string {
	if d := os.Getenv("WENSHU_DATA_DIR"); d != "" {
		return d
	}
	home, e := os.UserHomeDir()
	if e != nil {
		return "wenshu-data"
	}
	return filepath.Join(home, ".wenshu-mcp")
}
func (a *App) SetGap(d time.Duration) { a.Client.Gap = d; a.Manager.Gap = d }
