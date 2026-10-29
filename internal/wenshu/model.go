package wenshu

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const Version = "1.0"
const Endpoint = "https://wenshu.court.gov.cn/website/parse/rest.q4w"
const ListURL = "https://wenshu.court.gov.cn/website/wenshu/181217BMTKHNT2W0/index.html"
const DetailURL = "https://wenshu.court.gov.cn/website/wenshu/181107ANFZ0BXSK4/index.html?docId="

var Shanghai = time.FixedZone("Asia/Shanghai", 8*60*60)

type Condition struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}
type Query struct {
	Keyword    string      `json:"keyword,omitempty"`
	StartDate  string      `json:"start_date,omitempty"`
	EndDate    string      `json:"end_date,omitempty"`
	Conditions []Condition `json:"conditions,omitempty"`
	Sort       string      `json:"sort,omitempty"`
}

func (q Query) Normalize() (Query, error) {
	q.Conditions = append([]Condition{}, q.Conditions...)
	seen := map[string]bool{}
	for _, c := range q.Conditions {
		if c.Key == "" || c.Value == "" {
			return q, fmt.Errorf("检索条件 key/value 不能为空")
		}
		if seen[c.Key] {
			return q, fmt.Errorf("重复条件 %s", c.Key)
		}
		seen[c.Key] = true
	}
	if q.Keyword != "" {
		if seen["s21"] {
			return q, fmt.Errorf("keyword 与 conditions.s21 不能同时设置")
		}
		q.Conditions = append(q.Conditions, Condition{"s21", q.Keyword})
		q.Keyword = ""
	}
	if q.StartDate != "" || q.EndDate != "" {
		if seen["cprq"] {
			return q, fmt.Errorf("日期范围与 conditions.cprq 不能同时设置")
		}
		q.Conditions = append(q.Conditions, Condition{"cprq", q.StartDate + " TO " + q.EndDate})
		q.StartDate = ""
		q.EndDate = ""
	}
	for _, c := range q.Conditions {
		if c.Key == "cprq" {
			p := strings.Split(c.Value, " TO ")
			if len(p) != 2 {
				return q, fmt.Errorf("cprq 必须是 YYYY-MM-DD TO YYYY-MM-DD")
			}
			a, e := time.Parse("2006-01-02", p[0])
			b, e2 := time.Parse("2006-01-02", p[1])
			if e != nil || e2 != nil || a.After(b) {
				return q, fmt.Errorf("无效裁判日期范围 %q", c.Value)
			}
		}
	}
	if q.Sort == "" {
		q.Sort = "s51:asc"
	}
	switch q.Sort {
	case "s51:asc", "s51:desc", "s50:asc", "s50:desc", "s52:asc", "s52:desc":
	default:
		return q, fmt.Errorf("不支持的排序 %q", q.Sort)
	}
	return q, nil
}
func (q Query) Get(key string) string {
	for _, c := range q.Conditions {
		if c.Key == key {
			return c.Value
		}
	}
	return ""
}
func (q Query) With(key, value string) Query {
	out := q
	out.Conditions = append([]Condition{}, q.Conditions...)
	for i, c := range out.Conditions {
		if c.Key == key {
			out.Conditions[i].Value = value
			return out
		}
	}
	out.Conditions = append(out.Conditions, Condition{key, value})
	return out
}

type Record map[string]any

func str(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case json.Number:
		return x.String()
	default:
		b, _ := json.Marshal(x)
		return string(b)
	}
}
func field(m Record, keys ...string) string {
	for _, k := range keys {
		if v := str(m[k]); v != "" && v != "null" {
			return v
		}
	}
	return ""
}

type SearchResult struct {
	Total    int      `json:"total"`
	Records  []Record `json:"records"`
	Page     int      `json:"page"`
	PageSize int      `json:"page_size"`
}
type Facet struct {
	Value string `json:"value"`
	Count int    `json:"count"`
}
type JobSpec struct {
	Name        string   `json:"name"`
	Query       Query    `json:"query"`
	FetchBody   bool     `json:"fetch_body"`
	PageSize    int      `json:"page_size,omitempty"`
	SplitFields []string `json:"split_fields,omitempty"`
}
type Job struct {
	ID          string         `json:"id"`
	Spec        JobSpec        `json:"spec"`
	Status      string         `json:"status"`
	Error       string         `json:"error,omitempty"`
	Created     string         `json:"created_at"`
	Updated     string         `json:"updated_at"`
	Documents   int            `json:"documents"`
	Bodies      int            `json:"bodies"`
	Unavailable int            `json:"unavailable_bodies"`
	Shards      map[string]int `json:"shards"`
}
type Shard struct {
	ID       int64  `json:"id"`
	JobID    string `json:"job_id"`
	Parent   int64  `json:"parent"`
	Query    Query  `json:"query"`
	Status   string `json:"status"`
	Total    int    `json:"total"`
	NextPage int    `json:"next_page"`
	Error    string `json:"error,omitempty"`
}
type Paragraph struct {
	Number  int    `json:"number"`
	Section string `json:"section"`
	Text    string `json:"text"`
	Start   int    `json:"start"`
	End     int    `json:"end"`
}
type Document struct {
	ID           string      `json:"doc_id"`
	RemoteID     string      `json:"remote_id,omitempty"`
	Title        string      `json:"title"`
	Court        string      `json:"court"`
	CaseNumber   string      `json:"case_number"`
	JudgmentDate string      `json:"judgment_date"`
	CaseType     string      `json:"case_type"`
	DocType      string      `json:"doc_type"`
	BodyStatus   string      `json:"body_status"`
	Text         string      `json:"text,omitempty"`
	Metadata     Record      `json:"metadata"`
	Detail       Record      `json:"detail,omitempty"`
	Paragraphs   []Paragraph `json:"paragraphs,omitempty"`
	Updated      string      `json:"updated_at"`
}

func sourceURL(d Document) string {
	id := d.RemoteID
	if id == "" {
		id = d.ID
	}
	return DetailURL + url.QueryEscape(id)
}

func now() string           { return time.Now().In(Shanghai).Format(time.RFC3339) }
func jsonText(v any) string { b, _ := json.Marshal(v); return string(b) }
