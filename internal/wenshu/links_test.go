package wenshu

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func linkApp(t *testing.T) *App {
	t.Helper()
	a, e := OpenApp(context.Background(), t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { a.Close() })
	return a
}
func linkDoc(t *testing.T, a *App, id, title, number string) {
	t.Helper()
	if e := a.Store.SaveDetail(id, Record{"s5": id, "s1": title, "s7": number, "s2": "示例法院", "qwContent": "文书正文。"}); e != nil {
		t.Fatal(e)
	}
}
func TestLinkTitleCaseAndProtectedMarkdown(t *testing.T) {
	a := linkApp(t)
	linkDoc(t, a, "doc+/=", "甲与乙民事判决书", "（2020）京0105民初123号")
	input := "参见《甲与乙民事判决书》及(2020)京0105民初123号。\n`《甲与乙民事判决书》`\n[《甲与乙民事判决书》](https://example.com/a(b))\n```text\n《甲与乙民事判决书》\n```\n~~~~\n(2020)京0105民初123号\n~~~~\n    《甲与乙民事判决书》\n[《甲与乙民事判决书》][ref]\n[ref]: https://example.com\n"
	out, e := a.LinkReferences(context.Background(), LinkOptions{Text: input})
	if e != nil {
		t.Fatal(e)
	}
	if len(out.References) != 2 {
		t.Fatalf("references=%+v", out.References)
	}
	for _, r := range out.References {
		if r.Status != "linked" {
			t.Fatal(r)
		}
		u, e := url.Parse(r.URL)
		if e != nil || u.Query().Get("docId") != "doc+/=" {
			t.Fatal(r, e)
		}
	}
	tail := input[strings.Index(input, "\n"):]
	if !strings.HasSuffix(out.Text, tail) {
		t.Fatal("code or existing links changed", out.Text)
	}
}
func TestLinkNestedTitlesAmbiguityAndUnknown(t *testing.T) {
	a := linkApp(t)
	title := "甲公司与乙公司《〈合同〉效力》纠纷民事判决书"
	linkDoc(t, a, "d1", title, "（2021）沪01民终1号")
	out, e := a.LinkReferences(context.Background(), LinkOptions{Text: "见《" + title + "》及《不存在的文书》。"})
	if e != nil {
		t.Fatal(e)
	}
	if len(out.References) != 2 || out.References[0].Status != "linked" || out.References[1].Status != "unresolved" {
		t.Fatal(out)
	}
	linkDoc(t, a, "d2", title, "（2021）沪01民终2号")
	out, e = a.LinkReferences(context.Background(), LinkOptions{Text: "《" + title + "》"})
	if e != nil || out.References[0].Status != "ambiguous" || out.Text != "《"+title+"》" {
		t.Fatal(out, e)
	}
}
func TestLinkFullWidthAndLocalTaskScope(t *testing.T) {
	a := linkApp(t)
	linkDoc(t, a, "d1", "甲诉乙案", "（2020）京0105民初123号")
	out, e := a.LinkReferences(context.Background(), LinkOptions{Text: "【２０２０】京０１０５民初１２３号"})
	if e != nil || len(out.References) != 1 || out.References[0].Status != "linked" {
		t.Fatal(out, e)
	}
	j, e := a.Store.CreateJob(JobSpec{Name: "empty", Query: Query{}})
	if e != nil {
		t.Fatal(e)
	}
	out, e = a.LinkReferences(context.Background(), LinkOptions{Text: "《甲诉乙案》", JobID: j.ID})
	if e != nil || out.References[0].Status != "unresolved" {
		t.Fatal(out, e)
	}
}
func TestLinkLookupExactAndCache(t *testing.T) {
	a := linkApp(t)
	if e := SaveSession(a.Client.SessionPath, Session{Cookie: "test=ok"}); e != nil {
		t.Fatal(e)
	}
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		r.ParseForm()
		var result any
		switch {
		case strings.HasSuffix(r.Form.Get("cfg"), "@queryDoc"):
			result = map[string]any{"queryResult": map[string]any{"resultCount": 1, "resultList": []Record{{"rowkey": "temporary", "1": "网络文书", "7": "（2020）京0105民初123号"}}}}
		case strings.HasSuffix(r.Form.Get("cfg"), "@docInfoSearch"):
			result = Record{"s5": "stable", "s1": "网络文书", "s7": "（2020）京0105民初123号", "qwContent": "正文"}
		default:
			t.Error(r.Form)
		}
		json.NewEncoder(w).Encode(map[string]any{"success": true, "code": 1, "result": result})
	}))
	defer srv.Close()
	a.Client.URL = srv.URL
	a.Client.Gap = 0
	out, e := a.LinkReferences(context.Background(), LinkOptions{Text: "《网络文书》及（2020）京0105民初123号", LookupMissing: true})
	if e != nil {
		t.Fatal(e)
	}
	if calls != 2 || out.Lookups != 1 || len(out.References) != 2 {
		t.Fatal(out, calls)
	}
	for _, r := range out.References {
		if r.Status != "linked" || r.DocIDs[0] != "stable" {
			t.Fatal(r)
		}
	}
	if _, e = a.Store.Document("temporary"); e != nil {
		t.Fatal(e)
	}
}
func TestLinkBoundsAndHTMLProtection(t *testing.T) {
	a := linkApp(t)
	linkDoc(t, a, "d", "甲文书", "（2020）京民初1号")
	out, e := a.LinkReferences(context.Background(), LinkOptions{Text: "<code>《甲文书》</code> <!-- 《甲文书》 --> 《甲文书》"})
	if e != nil || len(out.References) != 1 {
		t.Fatal(out, e)
	}
	if _, e = a.LinkReferences(context.Background(), LinkOptions{Text: strings.Repeat("文", 512*1024)}); e == nil {
		t.Fatal("size bound")
	}
	if _, e = a.LinkReferences(context.Background(), LinkOptions{Text: strings.Repeat("《甲文书》", 101)}); e == nil {
		t.Fatal("count bound")
	}
}
