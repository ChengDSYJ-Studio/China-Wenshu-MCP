package wenshu

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

func TestLiveProtocolDiagnostic(t *testing.T) {
	cap := os.Getenv("WENSHU_DIAGNOSTIC_CAPTURE")
	if cap == "" {
		t.Skip("opt-in protocol diagnostic")
	}
	path := filepath.Join(t.TempDir(), "s.json")
	if e := ImportSession(cap, path); e != nil {
		t.Fatal(e)
	}
	c := NewClient(path)
	q, _ := (Query{Keyword: "未成年人", StartDate: "2020-01-01", EndDate: "2020-01-01"}).Normalize()
	for _, size := range []string{"", "15", "5", "15"} {
		var out Record
		p := url.Values{"queryCondition": {jsonText(q.Conditions)}, "pageNum": {"1"}, "sortFields": {"s50:desc"}, "s21": {"未成年人"}}
		if size != "" {
			p.Set("pageSize", size)
		}
		e := c.call(context.Background(), "queryDoc", p, &out)
		if e != nil {
			t.Fatal(e)
		}
		qr, _ := out["queryResult"].(map[string]any)
		t.Logf("size=%s total=%v params=%s", size, qr["resultCount"], jsonText(out["queryParams"]))
	}
}
