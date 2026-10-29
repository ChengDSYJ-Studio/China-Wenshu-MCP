package wenshu

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

type fakeAPI struct {
	rows        []Record
	failPage    int
	failBody    string
	bodyCalls   map[string]int
	searchCalls int
	shortPage   int
	badFacets   bool
	duplicates  bool
	drift       bool
}

func fixtureRows(n int) []Record {
	rows := []Record{}
	for i := range n {
		day := "2020-01-01"
		province := "甲省"
		if i >= n/2 {
			day = "2020-01-02"
			province = "乙省"
		}
		rows = append(rows, Record{"rowkey": fmt.Sprintf("doc-%05d", i), "1": "测试裁判文书", "31": day, "s33": province, "s42": "2020"})
	}
	return rows
}
func (f *fakeAPI) filtered(q Query) []Record {
	out := []Record{}
	q, _ = q.Normalize()
	for _, d := range f.rows {
		ok := true
		for _, c := range q.Conditions {
			if c.Key == "s21" {
				continue
			}
			if c.Key == "cprq" {
				p := strings.Split(c.Value, " TO ")
				day := field(d, "31")
				if day < p[0] || day > p[1] {
					ok = false
				}
			} else if field(d, c.Key) != c.Value {
				ok = false
			}
		}
		if ok {
			out = append(out, d)
		}
	}
	return out
}
func (f *fakeAPI) Search(ctx context.Context, q Query, page, size int) (SearchResult, error) {
	if e := ctx.Err(); e != nil {
		return SearchResult{}, e
	}
	f.searchCalls++
	if page == f.failPage {
		return SearchResult{}, fmt.Errorf("temporary list failure")
	}
	all := f.filtered(q)
	n := len(all)
	start := min((page-1)*size, n)
	end := min(start+size, n)
	records := append([]Record{}, all[start:end]...)
	if page == f.shortPage {
		records = nil
	}
	if f.duplicates && page > 1 && len(records) > 0 {
		records[0] = all[0]
	}
	if f.drift && page > 1 {
		n++
	}
	return SearchResult{n, records, page, size}, nil
}
func (f *fakeAPI) Facets(ctx context.Context, q Query, key string) ([]Facet, error) {
	counts := map[string]int{}
	for _, r := range f.filtered(q) {
		if v := field(r, key); v != "" {
			counts[v]++
		}
	}
	out := []Facet{}
	for v, n := range counts {
		if f.badFacets {
			n--
		}
		out = append(out, Facet{v, n})
	}
	return out, nil
}
func (f *fakeAPI) Detail(ctx context.Context, id string) (Record, error) {
	if f.bodyCalls == nil {
		f.bodyCalls = map[string]int{}
	}
	f.bodyCalls[id]++
	if id == f.failBody {
		return nil, errors.New("body interrupted")
	}
	return Record{"s5": id, "s1": "测试文书", "s25": "未成年人测试事实。", "qwContent": "<div>测试文书</div><p>未成年人测试事实。</p><p>本院认为：依法处理。</p>"}, nil
}
func testStore(t *testing.T) *Store {
	t.Helper()
	s, e := OpenStore(filepath.Join(t.TempDir(), "corpus.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func jobFor(t *testing.T, s *Store, q Query, body bool) Job {
	t.Helper()
	j, e := s.CreateJob(JobSpec{Name: "test", Query: q, FetchBody: body, PageSize: 15})
	if e != nil {
		t.Fatal(e)
	}
	return j
}
func TestCollectionBoundaries(t *testing.T) {
	for _, n := range []int{0, 1, 15, 16, 600, 601, 1201} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			s := testStore(t)
			api := &fakeAPI{rows: fixtureRows(n)}
			j := jobFor(t, s, Query{StartDate: "2020-01-01", EndDate: "2020-01-02"}, false)
			r := Runner{s, api}
			if e := r.Run(context.Background(), j.ID, RunOptions{}); e != nil {
				t.Fatal(e)
			}
			got, _ := s.Job(j.ID)
			if n == 1201 {
				if got.Status != "partial" {
					t.Fatalf("want partial with single unsplittable day: %+v", got)
				}
			} else if got.Status != "completed" || got.Documents != n {
				t.Fatalf("%+v", got)
			}
			sh, _ := s.Shards(j.ID)
			for _, x := range sh {
				if x.Status == "completed" && x.Total > 600 {
					t.Fatalf("completed oversized shard %+v", x)
				}
			}
		})
	}
}
func TestFacetSplitAndNoDateFilter(t *testing.T) {
	s := testStore(t)
	api := &fakeAPI{rows: fixtureRows(800)}
	for _, r := range api.rows {
		r["31"] = "2020-01-01"
	}
	j := jobFor(t, s, Query{}, false)
	if e := (&Runner{s, api}).Run(context.Background(), j.ID, RunOptions{}); e != nil {
		t.Fatal(e)
	}
	got, _ := s.Job(j.ID)
	if got.Status != "completed" || got.Documents != 800 {
		t.Fatalf("%+v", got)
	}
}
func TestRejectIncompleteFacets(t *testing.T) {
	s := testStore(t)
	api := &fakeAPI{rows: fixtureRows(800), badFacets: true}
	j := jobFor(t, s, Query{}, false)
	if e := (&Runner{s, api}).Run(context.Background(), j.ID, RunOptions{}); e != nil {
		t.Fatal(e)
	}
	got, _ := s.Job(j.ID)
	if got.Status != "partial" || got.Documents != 0 {
		t.Fatalf("%+v", got)
	}
}
func TestResumeAfterBodyFailureAndReopen(t *testing.T) {
	dir := t.TempDir()
	s, e := OpenStore(filepath.Join(dir, "corpus.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	api := &fakeAPI{rows: fixtureRows(20), failBody: "doc-00001"}
	j := jobFor(t, s, Query{}, true)
	r := Runner{s, api}
	if e = r.Run(context.Background(), j.ID, RunOptions{}); e == nil {
		t.Fatal("want failure")
	}
	sh, _ := s.Shards(j.ID)
	if sh[0].NextPage != 2 {
		t.Fatalf("page checkpoint %+v", sh)
	}
	s.Close()
	s, e = OpenStore(filepath.Join(dir, "corpus.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	api.failBody = ""
	r.Store = s
	if e = r.Run(context.Background(), j.ID, RunOptions{}); e != nil {
		t.Fatal(e)
	}
	got, _ := s.Job(j.ID)
	if got.Status != "completed" || got.Documents != 20 || got.Bodies != 20 {
		t.Fatalf("%+v", got)
	}
	if api.bodyCalls["doc-00000"] != 1 {
		t.Fatal("already saved body was fetched again")
	}
}
func TestFailureDoesNotAdvanceAndExplicitPause(t *testing.T) {
	s := testStore(t)
	api := &fakeAPI{rows: fixtureRows(30), shortPage: 2}
	j := jobFor(t, s, Query{}, false)
	r := Runner{s, api}
	if e := r.Run(context.Background(), j.ID, RunOptions{}); e == nil {
		t.Fatal("want short page error")
	}
	sh, _ := s.Shards(j.ID)
	if sh[0].NextPage != 2 {
		t.Fatal(sh)
	}
	api.shortPage = 0
	if e := r.Run(context.Background(), j.ID, RunOptions{MaxPages: 1}); e != nil {
		t.Fatal(e)
	}
	got, _ := s.Job(j.ID)
	if got.Status != "completed" {
		t.Fatal(got)
	}
	j = jobFor(t, s, Query{}, false)
	if e := r.Run(context.Background(), j.ID, RunOptions{MaxPages: 1}); !errors.Is(e, errPaused) {
		t.Fatalf("want pause, got %v", e)
	}
	if e := r.Run(context.Background(), j.ID, RunOptions{}); e != nil {
		t.Fatal(e)
	}
}
func TestDuplicateAndCountDriftArePartial(t *testing.T) {
	for _, kind := range []string{"duplicate", "drift"} {
		t.Run(kind, func(t *testing.T) {
			s := testStore(t)
			api := &fakeAPI{rows: fixtureRows(30), duplicates: kind == "duplicate", drift: kind == "drift"}
			j := jobFor(t, s, Query{}, false)
			if e := (&Runner{s, api}).Run(context.Background(), j.ID, RunOptions{}); e != nil {
				t.Fatal(e)
			}
			got, _ := s.Job(j.ID)
			if got.Status != "partial" {
				t.Fatal(got)
			}
		})
	}
}
func TestPageTransactionAndLease(t *testing.T) {
	s := testStore(t)
	j := jobFor(t, s, Query{}, false)
	sh, _ := s.NextShard(j.ID)
	e := s.SavePage(sh, SearchResult{Total: 2, Records: []Record{{"rowkey": "valid"}, {"1": "no id"}}, Page: 1, PageSize: 15})
	if e == nil {
		t.Fatal("want missing ID error")
	}
	got, _ := s.Job(j.ID)
	if got.Documents != 0 {
		t.Fatal("partial page committed")
	}
	if e = s.Acquire(context.Background(), "one"); e != nil {
		t.Fatal(e)
	}
	if e = s.Acquire(context.Background(), "two"); e == nil {
		t.Fatal("second collector acquired lock")
	}
	s.Release("one")
	if e = s.Acquire(context.Background(), "two"); e != nil {
		t.Fatal(e)
	}
}
