package wenshu

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// Opt-in only: a handful of real requests using the user's local session.
func TestLiveHTTP(t *testing.T) {
	capture := os.Getenv("WENSHU_LIVE_CAPTURE")
	if capture == "" {
		t.Skip("set WENSHU_LIVE_CAPTURE for live smoke test")
	}
	session := filepath.Join(t.TempDir(), "session.json")
	if e := ImportSession(capture, session); e != nil {
		t.Fatal(e)
	}
	c := NewClient(session)
	if os.Getenv("WENSHU_TEST_NEW_TOKEN") == "1" {
		s, e := readSession(session)
		if e != nil {
			t.Fatal(e)
		}
		s.Token = ""
		if e = SaveSession(session, s); e != nil {
			t.Fatal(e)
		}
	}
	q := Query{Keyword: "未成年人", StartDate: "2020-01-01", EndDate: "2020-01-01"}
	rs, e := c.Search(context.Background(), q, 1, 5)
	if e != nil {
		t.Fatal(e)
	}
	t.Logf("HTTP search: total=%d records=%d", rs.Total, len(rs.Records))
	fs, e := c.Facets(context.Background(), q, "s33")
	if e != nil {
		t.Fatal(e)
	}
	t.Logf("HTTP facets: groups=%d", len(fs))
	if len(rs.Records) > 0 {
		id := field(rs.Records[0], "rowkey")
		d, e := c.Detail(context.Background(), id)
		if e != nil {
			t.Fatal(e)
		}
		text, ps := ExtractParagraphs(d)
		t.Logf("HTTP detail: paragraphs=%d bytes=%d", len(ps), len(text))
		if len(text) == 0 {
			t.Fatal("body empty")
		}
	}
}
