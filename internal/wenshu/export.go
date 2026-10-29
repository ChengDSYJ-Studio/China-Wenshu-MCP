package wenshu

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type ExportOptions struct {
	Directory string `json:"directory"`
	JobID     string `json:"job_id,omitempty"`
	Book      string `json:"book,omitempty"`
	Version   string `json:"version,omitempty"`
	Coder     string `json:"coder,omitempty"`
	Format    string `json:"format,omitempty"`
}
type ExportResult struct {
	Directory  string   `json:"directory"`
	Documents  int      `json:"documents"`
	Paragraphs int      `json:"paragraphs"`
	Codings    int      `json:"codings"`
	Files      []string `json:"files"`
}
type tableWriter struct {
	cols           []Column
	spool, csvFile *os.File
	enc            *json.Encoder
	csv            *csv.Writer
	name           string
	count          int
}

func newTable(dir, name string, cols []Column) (*tableWriter, error) {
	s, e := os.Create(filepath.Join(dir, "."+name+".jsonl"))
	if e != nil {
		return nil, e
	}
	f, e := os.Create(filepath.Join(dir, name+".csv"))
	if e != nil {
		s.Close()
		return nil, e
	}
	w := &tableWriter{cols: cols, spool: s, csvFile: f, enc: json.NewEncoder(s), csv: csv.NewWriter(f), name: name}
	headers := []string{}
	for _, c := range cols {
		headers = append(headers, c.Name)
	}
	if e = w.csv.Write(headers); e != nil {
		w.close()
		return nil, e
	}
	return w, nil
}
func (w *tableWriter) add(row []any) error {
	if len(row) != len(w.cols) {
		return fmt.Errorf("表 %s 列数错误", w.name)
	}
	if e := w.enc.Encode(row); e != nil {
		return e
	}
	text := make([]string, len(row))
	for i, v := range row {
		text[i] = str(v)
	}
	if e := w.csv.Write(text); e != nil {
		return e
	}
	w.count++
	return nil
}
func (w *tableWriter) close() error {
	w.csv.Flush()
	e := w.csv.Error()
	if e2 := w.csvFile.Close(); e == nil {
		e = e2
	}
	if e2 := w.spool.Close(); e == nil {
		e = e2
	}
	return e
}
func (s *Store) Export(opt ExportOptions) (result ExportResult, err error) {
	if opt.Directory == "" {
		return result, fmt.Errorf("导出目录不能为空")
	}
	if opt.Format == "" {
		opt.Format = "both"
	}
	if opt.Format != "csv" && opt.Format != "dta" && opt.Format != "both" {
		return result, fmt.Errorf("format 必须是 csv、dta 或 both")
	}
	dest, e := filepath.Abs(opt.Directory)
	if e != nil {
		return result, e
	}
	if _, e = os.Stat(dest); !os.IsNotExist(e) {
		return result, fmt.Errorf("导出目录已存在，请使用新目录：%s", dest)
	}
	if e = os.MkdirAll(filepath.Dir(dest), 0700); e != nil {
		return result, e
	}
	stage, e := os.MkdirTemp(filepath.Dir(dest), ".wenshu-export-*")
	if e != nil {
		return result, e
	}
	defer os.RemoveAll(stage)
	// A database snapshot makes all exported tables and counts refer to one state.
	snapshot := filepath.Join(stage, "snapshot.sqlite")
	if _, e = s.DB.Exec(`VACUUM INTO ?`, snapshot); e != nil {
		return result, e
	}
	snap, e := OpenStore(snapshot)
	if e != nil {
		return result, e
	}
	defer snap.Close()
	if opt.JobID != "" {
		if _, e = snap.Job(opt.JobID); e != nil {
			return result, e
		}
	}
	var book Codebook
	if opt.Book != "" {
		if opt.Version == "" {
			return result, fmt.Errorf("指定 book 时必须指定 version")
		}
		book, e = snap.Codebook(opt.Book, opt.Version)
		if e != nil {
			return result, e
		}
		if opt.Coder == "" {
			return result, fmt.Errorf("宽表编码导出需指定 coder，避免覆盖不同编码员的结果")
		}
	}
	cols := []Column{{"doc_id", "文书ID", false}, {"title", "案件名称", false}, {"court", "法院", false}, {"case_number", "案号", false}, {"judgment_date", "裁判日期 ISO", false}, {"case_type", "案件类型原始值", false}, {"doc_type", "文书类型原始值", false}, {"body_status", "正文状态", false}, {"text", "正文", false}, {"source_url", "来源链接", false}, {"updated_at", "本地最后更新时间", false}}
	seen := map[string]bool{}
	for _, c := range cols {
		seen[c.Name] = true
	}
	for _, v := range book.Variables {
		if v.Unit != "document" {
			continue
		}
		if seen[v.Name] {
			return result, fmt.Errorf("编码变量 %s 与导出基础字段重名", v.Name)
		}
		seen[v.Name] = true
		cols = append(cols, Column{v.Name, v.Label, v.Type == "number" || v.Type == "integer" || v.Type == "boolean"})
	}
	docs, e := newTable(stage, "documents", cols)
	if e != nil {
		return result, e
	}
	closed := false
	defer func() {
		if !closed {
			docs.close()
		}
	}()
	paras, e := newTable(stage, "paragraphs", []Column{{"doc_id", "文书ID", false}, {"paragraph", "段落号", true}, {"section", "来源段落类型", false}, {"text", "段落原文", false}, {"start", "起始字符偏移", true}, {"end", "结束字符偏移", true}})
	if e != nil {
		return result, e
	}
	defer func() {
		if !closed {
			paras.close()
		}
	}()
	codes, e := newTable(stage, "codings", []Column{{"doc_id", "文书ID", false}, {"book", "Codebook", false}, {"version", "版本", false}, {"variable", "变量", false}, {"unit", "0=文书；正整数=段落号", true}, {"coder", "编码员", false}, {"value_json", "JSON 编码值", false}, {"evidence_json", "原文证据 JSON", false}, {"source", "编码方法", false}, {"note", "备注", false}, {"updated_at", "编码时间", false}, {"stale", "正文变更待复核", true}})
	if e != nil {
		return result, e
	}
	defer func() {
		if !closed {
			codes.close()
		}
	}()
	raw, e := os.Create(filepath.Join(stage, "corpus.jsonl"))
	if e != nil {
		return result, e
	}
	defer raw.Close()
	rawEnc := json.NewEncoder(raw)
	offset := 0
	for {
		ids, e := snap.DocumentIDs(opt.JobID, "", 500, offset)
		if e != nil {
			return result, e
		}
		if len(ids) == 0 {
			break
		}
		offset += len(ids)
		for _, id := range ids {
			d, e := snap.Document(id)
			if e != nil {
				return result, e
			}
			wide, e := snap.wideValues(id, opt.Book, opt.Version, opt.Coder)
			if e != nil {
				return result, e
			}
			row := []any{d.ID, d.Title, d.Court, d.CaseNumber, d.JudgmentDate, d.CaseType, d.DocType, d.BodyStatus, d.Text, sourceURL(d), d.Updated}
			for _, v := range book.Variables {
				if v.Unit == "document" {
					row = append(row, wide[v.Name])
				}
			}
			if e = docs.add(row); e != nil {
				return result, e
			}
			for _, p := range d.Paragraphs {
				if e = paras.add([]any{id, p.Number, p.Section, p.Text, p.Start, p.End}); e != nil {
					return result, e
				}
			}
			if e = rawEnc.Encode(d); e != nil {
				return result, e
			}
		}
	}
	if e = raw.Close(); e != nil {
		return result, e
	}
	if e = snap.exportCodingHistory(filepath.Join(stage, "coding_history.jsonl"), opt.JobID); e != nil {
		return result, e
	}
	e = snap.eachCoding(opt.JobID, opt.Book, opt.Version, opt.Coder, -1, 0, func(c Coding) error {
		stale := 0
		if c.Stale {
			stale = 1
		}
		return codes.add([]any{c.DocID, c.Book, c.Version, c.Variable, c.Unit, c.Coder, jsonText(c.Value), jsonText(c.Evidence), c.Source, c.Note, c.Updated, stale})
	})
	if e != nil {
		return result, e
	}
	closed = true
	for _, w := range []*tableWriter{docs, paras, codes} {
		if e = w.close(); e != nil {
			return result, e
		}
		if opt.Format != "csv" {
			if e = WriteDTA(filepath.Join(stage, w.name+".dta"), w.spool.Name(), w.cols); e != nil {
				return result, e
			}
		}
		if opt.Format == "dta" {
			if e = os.Remove(w.csvFile.Name()); e != nil {
				return result, e
			}
		}
		if e = os.Remove(w.spool.Name()); e != nil {
			return result, e
		}
	}
	books, e := snap.Codebooks()
	if e != nil {
		return result, e
	}
	if opt.Book != "" {
		books = []Codebook{book}
	}
	if e = atomicWrite(filepath.Join(stage, "codebooks.json"), []byte(jsonText(books))); e != nil {
		return result, e
	}
	if e = atomicWrite(filepath.Join(stage, "variables.json"), []byte(jsonText(map[string]any{"documents": docs.cols, "paragraphs": paras.cols, "codings": codes.cols}))); e != nil {
		return result, e
	}
	methods, e := snap.Methods(opt.JobID)
	if e != nil {
		return result, e
	}
	methods += fmt.Sprintf("\n## 本次导出\n\n导出时间：%s。筛选任务：%s。宽表 codebook：%s，版本：%s，编码员：%s。文书 %d，段落 %d，编码 %d。分类编码以原始代码保留，取值标签见 codebooks.json。缺失数值在 Stata 中为 .，字符串缺失为空串；长正文为 strL。待复核编码保留在 codings 表且不进入宽表。\n", now(), opt.JobID, opt.Book, opt.Version, opt.Coder, docs.count, paras.count, codes.count)
	if e = atomicWrite(filepath.Join(stage, "methods.md"), []byte(methods)); e != nil {
		return result, e
	}
	do := `* 在导出目录执行。Stata 14+；documents.dta 已包含 UTF-8 和长正文。
clear all
use "documents.dta", clear
describe
* 编码规则与值标签见 codebooks.json，证据见 codings.dta。
`
	if opt.Format == "csv" {
		do = `* 在导出目录执行。保留案号、文书ID与日期字符串。
clear all
import delimited using "documents.csv", clear encoding("utf-8") varnames(1) stringcols(_all) bindquote(strict) maxquotedrows(unlimited)
save "documents.dta", replace
`
	}
	if e = atomicWrite(filepath.Join(stage, "open_in_stata.do"), []byte(do)); e != nil {
		return result, e
	}
	if e = snap.Close(); e != nil {
		return result, e
	}
	for _, p := range []string{snapshot, snapshot + "-wal", snapshot + "-shm"} {
		if e = os.Remove(p); e != nil && !os.IsNotExist(e) {
			return result, e
		}
	}
	entries, e := os.ReadDir(stage)
	if e != nil {
		return result, e
	}
	result = ExportResult{Directory: dest, Documents: docs.count, Paragraphs: paras.count, Codings: codes.count, Files: []string{}}
	for _, x := range entries {
		result.Files = append(result.Files, filepath.Join(dest, x.Name()))
	}
	sort.Strings(result.Files)
	if e = os.Rename(stage, dest); e != nil {
		return result, e
	}
	return result, nil
}

func (s *Store) Methods(job string) (string, error) {
	jobs, e := s.Jobs()
	if e != nil {
		return "", e
	}
	if job != "" {
		j, e := s.Job(job)
		if e != nil {
			return "", e
		}
		jobs = []Job{j}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# 裁判文书采集与编码方法附录\n\n生成时间：%s；程序版本：%s。来源：中国裁判文书网。\n\n检索与正文请求由 Go HTTP 客户端完成；浏览器仅用于用户登录和更新会话。默认每页 5 条（网站在 10/15 条模式下可能丢失日期条件），单分片可访问上限按现场验证为 600 条。请求串行执行，code 9 与暂时性 HTTP 错误采用退避重试。每个任务记录检索条件、分片、页码与请求尝试。\n\n超过 600 条时，优先二分裁判日期；没有日期区间时从年份等互斥分类开始。分类总数与逐项查询计数须和父集相等才采用；无法拆分或分页去重数不符时保留 blocked 分片，并将任务标为 partial。\n\n取得正文后按稳定的 s5 归并文书；临时 rowkey 保留为访问别名，跨任务保留任务归属。尚未取得正文的列表记录没有确认稳定身份，不按案号或标题猜测合并。归并前编码保留在 coding_history.jsonl。分页与页码在同一 SQLite 事务提交；正文逐篇保存。正文优先取 qwContent，缺失时合并网站段落字段；HTML 块和换行成为段落，空白规范化。段落类型仅根据网站字段与段落文本对应关系标记，未对应的保留“全文”。字符偏移是规范化全文中的 Unicode 码点，起点含、终点不含。原始 metadata/detail 保留在 corpus.jsonl。\n\n规则编码不等于人工判断；多取值命中报告冲突，不命中且没有明示 default 时保留缺失。编码记录版本、编码员、证据段落和引文。正文变化后既有编码标为待复核。\n\n覆盖限制：网站公开文书不是案件全集；撤下、不公开、缺失正文与访问时点均可能影响样本。动态排序和索引更新可造成分页重复/漏失，计数核对不能证明绝对完整性。本工具不把无数据、会话失效或未完成分片当作成功。\n", now(), Version)
	for _, j := range jobs {
		fmt.Fprintf(&b, "\n## 任务 %s\n\n名称：%s；状态：%s；创建：%s；更新：%s。去重文书 %d，正文 %d，不公开正文 %d。\n\n```json\n%s\n```\n\n", j.ID, j.Spec.Name, j.Status, j.Created, j.Updated, j.Documents, j.Bodies, j.Unavailable, jsonText(j.Spec))
		if j.Error != "" {
			fmt.Fprintf(&b, "最近说明：%s\n\n", j.Error)
		}
		shards, e := s.Shards(j.ID)
		if e != nil {
			return "", e
		}
		b.WriteString("|分片|父分片|状态|接口计数|下一页|条件|说明|\n|---|---|---|---|---|---|---|\n")
		clean := func(x string) string { return strings.NewReplacer("|", "\\|", "\n", " ").Replace(x) }
		for _, sh := range shards {
			fmt.Fprintf(&b, "|%d|%d|%s|%d|%d|%s|%s|\n", sh.ID, sh.Parent, sh.Status, sh.Total, sh.NextPage, clean(jsonText(sh.Query)), clean(sh.Error))
		}
		var attempts, failures int
		var ms int64
		if e = s.DB.QueryRow(`SELECT count(*),coalesce(sum(error<>''),0),coalesce(sum(duration_ms),0) FROM requests WHERE job_id=?`, j.ID).Scan(&attempts, &failures, &ms); e != nil {
			return "", e
		}
		fmt.Fprintf(&b, "\nHTTP 尝试 %d 次，失败尝试 %d 次，累计请求耗时 %d ms（含重试，不含等待）。\n", attempts, failures, ms)
		rows, e := s.DB.Query(`SELECT at,kind,message FROM events WHERE job_id=? ORDER BY id`, j.ID)
		if e != nil {
			return "", e
		}
		for rows.Next() {
			var at, kind, msg string
			if e = rows.Scan(&at, &kind, &msg); e != nil {
				rows.Close()
				return "", e
			}
			fmt.Fprintf(&b, "\n- %s [%s] %s", at, kind, msg)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return "", e
		}
		b.WriteByte('\n')
	}
	return b.String(), nil
}

// Export pre-merge assessments so identity consolidation never hides a coder's history.
func (s *Store) exportCodingHistory(path, job string) error {
	f, e := os.Create(path)
	if e != nil {
		return e
	}
	defer f.Close()
	rows, e := s.DB.Query(`SELECT original_doc_id,canonical_doc_id,coding_json,merged_at FROM coding_merge_history h WHERE (?='' OR EXISTS(SELECT 1 FROM job_docs j WHERE j.job_id=? AND j.doc_id=h.canonical_doc_id)) ORDER BY id`, job, job)
	if e != nil {
		return e
	}
	defer rows.Close()
	enc := json.NewEncoder(f)
	for rows.Next() {
		var old, canonical, payload, at string
		if e = rows.Scan(&old, &canonical, &payload, &at); e != nil {
			return e
		}
		if e = enc.Encode(map[string]any{"original_doc_id": old, "doc_id": canonical, "coding": json.RawMessage(payload), "merged_at": at}); e != nil {
			return e
		}
	}
	if e = rows.Err(); e != nil {
		return e
	}
	return f.Close()
}
