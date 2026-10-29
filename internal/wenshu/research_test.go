package wenshu

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func writeBook(t *testing.T, s *Store) Codebook {
	t.Helper()
	b := Codebook{Name: "demo", Version: "1", Variables: []Variable{{Name: "has_minor", Label: "提及未成年人", Type: "boolean", Unit: "document", Rules: []Rule{{Pattern: "未成年人", Value: true}}, Default: false}, {Name: "amount", Label: "示例数值", Type: "number", Unit: "document", Rules: []Rule{{Pattern: "未成年人", Value: 12.5}}}, {Name: "group", Label: "类别", Type: "category", Unit: "document", Values: map[string]string{"minor": "未成年人", "other": "其他"}, Rules: []Rule{{Pattern: "未成年人", Value: "minor"}}, Default: "other"}, {Name: "paragraph_flag", Label: "段落命中", Type: "integer", Unit: "paragraph", Rules: []Rule{{Pattern: "未成年人", Value: 1}}}}}
	path := filepath.Join(t.TempDir(), "book.json")
	if e := os.WriteFile(path, []byte(jsonText(b)), 0600); e != nil {
		t.Fatal(e)
	}
	got, e := s.ImportCodebook(path)
	if e != nil {
		t.Fatal(e)
	}
	return got
}
func seedResearch(t *testing.T) (*Store, Job) {
	t.Helper()
	s := testStore(t)
	j := jobFor(t, s, Query{}, true)
	sh, _ := s.NextShard(j.ID)
	if e := s.SavePage(sh, SearchResult{Total: 2, Page: 1, PageSize: 15, Records: []Record{{"rowkey": "d1", "1": "含中文、引号\"与逗号的案件", "7": "(2020)案1号"}, {"rowkey": "d2", "1": "另一文书"}}}); e != nil {
		t.Fatal(e)
	}
	if e := s.SaveDetail("d1", Record{"s5": "d1", "qwContent": "<p>未成年人 &amp; \"证据\"。</p><div>" + strings.Repeat("长正文😀", 600) + "</div>"}); e != nil {
		t.Fatal(e)
	}
	if e := s.SaveDetail("d2", Record{"s5": "d2", "qwContent": "<p>普通案件。</p>"}); e != nil {
		t.Fatal(e)
	}
	writeBook(t, s)
	return s, j
}
func TestCodebookEvidenceAndStale(t *testing.T) {
	s, j := seedResearch(t)
	report, e := s.ApplyCodebook(j.ID, "demo", "1", "")
	if e != nil {
		t.Fatal(e)
	}
	if report.Written != 6 {
		t.Fatalf("%+v", report)
	}
	c := Coding{DocID: "d1", Book: "demo", Version: "1", Variable: "has_minor", Coder: "human", Value: true, Evidence: []Evidence{{Paragraph: 1, Quote: "假的证据"}}}
	if e = s.PutCoding(c); e == nil {
		t.Fatal("invalid evidence accepted")
	}
	c.Evidence[0].Quote = "未成年人"
	if e = s.PutCoding(c); e != nil {
		t.Fatal(e)
	}
	if e = s.SaveDetail("d1", Record{"s5": "d1", "qwContent": "<p>正文已更正。</p>"}); e != nil {
		t.Fatal(e)
	}
	codes, e := s.Codings(j.ID, "demo", "1", "human")
	if e != nil || len(codes) != 1 || !codes[0].Stale {
		t.Fatalf("%+v %v", codes, e)
	}
	b, _ := s.Codebook("demo", "1")
	b.Description = "changed"
	path := filepath.Join(t.TempDir(), "b.json")
	os.WriteFile(path, []byte(jsonText(b)), 0600)
	if _, e = s.ImportCodebook(path); e == nil {
		t.Fatal("modified version accepted")
	}
}
func TestExportSnapshotCSVAndStata(t *testing.T) {
	s, j := seedResearch(t)
	if _, e := s.ApplyCodebook(j.ID, "demo", "1", ""); e != nil {
		t.Fatal(e)
	}
	dir := os.Getenv("WENSHU_TEST_EXPORT")
	if dir == "" {
		dir = filepath.Join(t.TempDir(), "export")
	}
	out, e := s.Export(ExportOptions{Directory: dir, JobID: j.ID, Book: "demo", Version: "1", Coder: "rule:demo:1"})
	if e != nil {
		t.Fatal(e)
	}
	if out.Documents != 2 || out.Paragraphs != 3 || out.Codings != 6 {
		t.Fatalf("%+v", out)
	}
	for _, path := range out.Files {
		info, e := os.Stat(path)
		if e != nil || (info.Size() == 0 && filepath.Base(path) != "coding_history.jsonl") {
			t.Fatalf("empty file %s", path)
		}
	}
	if _, e = s.Export(ExportOptions{Directory: dir}); e == nil {
		t.Fatal("existing export overwritten")
	}
	if _, e = s.Export(ExportOptions{Directory: filepath.Join(t.TempDir(), "empty"), JobID: "missing"}); e == nil {
		t.Fatal("invalid job accepted")
	}
}
func TestEmptyDTAAndMissingBody(t *testing.T) {
	s := testStore(t)
	if _, e := s.Export(ExportOptions{Directory: filepath.Join(t.TempDir(), "empty")}); e != nil {
		t.Fatal(e)
	}
	if e := s.SaveDetail("private", Record{"s5": "private", "s43": "02", "s32": "不公开"}); e != nil {
		t.Fatal(e)
	}
	d, e := s.Document("private")
	if e != nil || d.BodyStatus != "unavailable" {
		t.Fatal(d, e)
	}
	if e = s.SaveDetail("empty", Record{"s5": "empty"}); e == nil {
		t.Fatal("empty unexplained body accepted")
	}
}
func TestParagraphOffsetsAndSections(t *testing.T) {
	text, ps := ExtractParagraphs(Record{"qwContent": "<p>首部😀</p><script>bad()</script><p>事实 &amp; 依据<br/>第二行</p>", "s25": "事实 & 依据\n第二行"})
	if len(ps) != 3 || strings.Contains(text, "bad") {
		t.Fatal(text, ps)
	}
	for _, p := range ps {
		if string([]rune(text)[p.Start:p.End]) != p.Text || p.End-p.Start != utf8.RuneCountInString(p.Text) {
			t.Fatal(p)
		}
	}
	if ps[1].Section != "事实" {
		t.Fatal(ps)
	}
}
func TestMCPToolsAndCalls(t *testing.T) {
	ctx := context.Background()
	a, e := OpenApp(ctx, t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	st, ct := mcp.NewInMemoryTransports()
	ss, e := a.Server().Connect(ctx, st, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer ss.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	cs, e := client.Connect(ctx, ct, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer cs.Close()
	list, e := cs.ListTools(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	if len(list.Tools) != 22 {
		t.Fatalf("tools=%d", len(list.Tools))
	}
	if e = a.Store.SaveDetail("mcp-doc", Record{"s5": "mcp-doc", "qwContent": "未成年人测试。"}); e != nil {
		t.Fatal(e)
	}
	writeBook(t, a.Store)
	manual, e := cs.CallTool(ctx, &mcp.CallToolParams{Name: "wenshu_code_put", Arguments: map[string]any{"doc_id": "mcp-doc", "book": "demo", "version": "1", "variable": "has_minor", "unit": 0, "coder": "human", "value": true, "evidence": []map[string]any{{"paragraph": 1, "quote": "未成年人"}}}})
	if e != nil || manual.IsError {
		t.Fatalf("manual coding schema: %+v %v", manual, e)
	}
	for _, tc := range []struct {
		name string
		args map[string]any
	}{{"wenshu_session_status", map[string]any{}}, {"wenshu_task_create", map[string]any{"name": "MCP test", "query": map[string]any{}, "fetch_body": false}}, {"wenshu_task_list", map[string]any{}}, {"wenshu_dictionary", map[string]any{"key": "ajlx"}}} {
		out, e := cs.CallTool(ctx, &mcp.CallToolParams{Name: tc.name, Arguments: tc.args})
		if e != nil || out.IsError {
			b, _ := json.Marshal(out)
			t.Fatalf("%s: %s %v", tc.name, b, e)
		}
	}
}
