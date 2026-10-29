package wenshu

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHTTPGeneratedParametersAndEncryptedReply(t *testing.T) {
	key := "123456789012345678901234"
	var cipher1 string
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "POST" {
			t.Error(r.Method)
		}
		if e := r.ParseForm(); e != nil {
			t.Error(e)
		}
		if !strings.HasSuffix(r.Form.Get("cfg"), "@queryDoc") || r.Form.Get("pageSize") != "15" {
			t.Error(r.Form)
		}
		if !strings.Contains(r.Header.Get("Cookie"), "test=hello") {
			t.Error("cookie missing")
		}
		if cipher1 == r.Form.Get("ciphertext") {
			t.Error("reused ciphertext")
		}
		cipher1 = r.Form.Get("ciphertext")
		raw := []byte(`{"queryResult":{"resultCount":1,"resultList":[{"rowkey":"d1"}]}}`)
		enc, e := encrypt3DES(raw, []byte(key), []byte(time.Now().In(Shanghai).Format("20060102")))
		if e != nil {
			t.Error(e)
		}
		json.NewEncoder(w).Encode(map[string]any{"success": true, "code": 1, "secretKey": key, "result": base64.StdEncoding.EncodeToString(enc)})
	}))
	defer srv.Close()
	path := filepath.Join(t.TempDir(), "session.json")
	if e := SaveSession(path, Session{Cookie: "test=hello", Token: "token"}); e != nil {
		t.Fatal(e)
	}
	c := NewClient(path)
	c.URL = srv.URL
	c.Gap = 0
	for range 2 {
		rs, e := c.Search(context.Background(), Query{Keyword: "合同"}, 1, 15)
		if e != nil || rs.Total != 1 {
			t.Fatalf("%+v %v", rs, e)
		}
	}
	if calls != 2 {
		t.Fatal(calls)
	}
}
func TestHTTPFailureClassification(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
	}{{"login", `{"success":false,"code":4,"description":"请登录"}`, 200}, {"busy", `{"success":false,"code":9}`, 200}, {"html", "<html>login</html>", 200}, {"schema", `{"success":true,"result":{}}`, 200}, {"http", "busy", 503}, {"redirect", "", 302}, {"badcrypto", `{"success":true,"secretKey":"123456789012345678901234","result":"aaaa"}`, 200}} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); fmt.Fprint(w, tc.body) }))
			defer srv.Close()
			path := filepath.Join(t.TempDir(), "s.json")
			SaveSession(path, Session{Cookie: "x=y"})
			c := NewClient(path)
			c.URL = srv.URL
			c.Retries = 0
			c.Gap = 0
			if _, e := c.Search(context.Background(), Query{}, 1, 15); e == nil {
				t.Fatal("want failure")
			}
		})
	}
}
func TestCipherMonthCompatibility(t *testing.T) {
	for month, expected := range map[time.Month]string{time.January: "20260129", time.September: "20260929", time.October: "2026929", time.November: "20261029", time.December: "20261129"} {
		date := time.Date(2026, month, 29, 12, 0, 0, 0, Shanghai)
		if got := cipherDate(date); got != expected {
			t.Fatalf("%s != %s", got, expected)
		}
	}
	if _, e := decryptResult(json.RawMessage(`"AAAA"`), "123456789012345678901234", time.Now()); e == nil {
		t.Fatal("want invalid cipher error")
	}
}
func TestQueryNormalization(t *testing.T) {
	for _, q := range []Query{{StartDate: "2020-02-30", EndDate: "2020-03-01"}, {Keyword: "a", Conditions: []Condition{{"s21", "b"}}}, {StartDate: "2021-01-01", EndDate: "2020-01-01"}, {Sort: "bad"}} {
		if _, e := q.Normalize(); e == nil {
			t.Fatal(q)
		}
	}
}

func TestDroppedDateFilterIsRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"success":true,"result":{"queryParams":{"pageNum":1,"pageSize":15,"queryItemList":[{"id":"s21","value":"未成年人","oper":"EQUAL"}]},"queryResult":{"resultCount":523156,"resultList":[]}}}`)
	}))
	defer srv.Close()
	path := filepath.Join(t.TempDir(), "s.json")
	SaveSession(path, Session{Cookie: "x=y"})
	c := NewClient(path)
	c.URL = srv.URL
	c.Gap = 0
	_, e := c.Search(context.Background(), Query{StartDate: "2020-01-01", EndDate: "2020-01-01"}, 1, 15)
	if e == nil || !strings.Contains(e.Error(), "实际检索条件") {
		t.Fatal(e)
	}
}

func TestRequestAuditStored(t *testing.T) {
	s := testStore(t)
	s.LogRequest("job", RequestLog{Operation: "queryDoc", Status: 200, Code: "1", Attempt: 1, DurationMS: 20})
	var n int
	if e := s.DB.QueryRow(`SELECT count(*) FROM requests WHERE job_id='job'`).Scan(&n); e != nil || n != 1 {
		t.Fatal(n, e)
	}
}
