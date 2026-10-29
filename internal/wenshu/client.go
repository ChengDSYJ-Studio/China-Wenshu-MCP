package wenshu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

var ErrSession = errors.New("会话失效或需要在登录浏览器完成验证；更新会话后续采")
var ErrQueryMismatch = errors.New("网站返回的实际检索条件与请求不一致")

type API interface {
	Search(context.Context, Query, int, int) (SearchResult, error)
	Facets(context.Context, Query, string) ([]Facet, error)
	Detail(context.Context, string) (Record, error)
}
type RequestLog struct {
	Operation  string
	Status     int
	Code       string
	Attempt    int
	DurationMS int64
	Error      string
}
type Client struct {
	SessionPath string
	URL         string
	HTTP        *http.Client
	Gap         time.Duration
	Retries     int
	Log         func(RequestLog)
	mu          sync.Mutex
	last        time.Time
	cookie      string
}

func NewClient(session string) *Client {
	jar, _ := cookiejar.New(nil)
	return &Client{SessionPath: session, URL: Endpoint, HTTP: &http.Client{Timeout: 40 * time.Second, Jar: jar, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}, Gap: 300 * time.Millisecond, Retries: 4}
}
func wait(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
func (c *Client) call(ctx context.Context, op string, p url.Values, out any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	s, e := readSession(c.SessionPath)
	if e != nil {
		return e
	}
	u, e := url.Parse(c.URL)
	if e != nil {
		return e
	}
	if c.cookie != s.Cookie {
		jar, _ := cookiejar.New(nil)
		c.HTTP.Jar = jar
		req := http.Request{Header: http.Header{"Cookie": []string{s.Cookie}}}
		c.HTTP.Jar.SetCookies(u, req.Cookies())
		c.cookie = s.Cookie
	}
	var last error
	for attempt := 0; attempt <= c.Retries; attempt++ {
		if e := wait(ctx, time.Until(c.last.Add(c.Gap))); e != nil {
			return e
		}
		if attempt > 0 {
			if e := wait(ctx, time.Duration(1<<min(attempt, 5))*time.Second); e != nil {
				return e
			}
		}
		v := url.Values{}
		for k, x := range p {
			v[k] = append([]string{}, x...)
		}
		ciphertext, e := GenerateCiphertext(time.Now())
		if e != nil {
			return e
		}
		v.Set("ciphertext", ciphertext)
		token := s.Token
		if token == "" {
			token, e = randomString(24)
			if e != nil {
				return e
			}
		}
		v.Set("__RequestVerificationToken", token)
		cfg := "com.lawyee.judge.dc.parse.dto.SearchDataDsoDTO@" + op
		if op == "currentUser" {
			cfg = "com.lawyee.wbsttools.web.parse.dto.AppUserDTO@currentUser"
		}
		v.Set("cfg", cfg)
		v.Set("wh", "900")
		v.Set("ww", "1440")
		v.Set("cs", "0")
		page, e := randomString(32)
		if e != nil {
			return e
		}
		v.Set("pageId", page)
		req, e := http.NewRequestWithContext(ctx, "POST", c.URL, strings.NewReader(v.Encode()))
		if e != nil {
			return e
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
		req.Header.Set("Accept", "application/json, text/javascript, */*; q=0.01")
		req.Header.Set("Origin", u.Scheme+"://"+u.Host)
		req.Header.Set("Referer", ListURL)
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
		ua := s.UserAgent
		if ua == "" {
			ua = "Mozilla/5.0 Chrome/131.0.0.0 Safari/537.36"
		}
		req.Header.Set("User-Agent", ua)
		started := time.Now()
		r, e := c.HTTP.Do(req)
		c.last = time.Now()
		log := RequestLog{Operation: op, Attempt: attempt + 1, DurationMS: time.Since(started).Milliseconds()}
		if e != nil {
			last = fmt.Errorf("HTTP 请求失败: %w", e)
			log.Error = "transport_error"
			c.record(log)
			if ctx.Err() != nil {
				return ctx.Err()
			}
			continue
		}
		log.Status = r.StatusCode
		b, re := io.ReadAll(io.LimitReader(r.Body, 32<<20))
		r.Body.Close()
		if re != nil {
			last = re
			log.Error = "read_error"
			c.record(log)
			continue
		}
		if r.StatusCode == 401 || r.StatusCode == 403 || r.StatusCode >= 300 && r.StatusCode < 400 {
			log.Error = "session_required"
			c.record(log)
			return ErrSession
		}
		if r.StatusCode == 429 || r.StatusCode >= 500 {
			last = fmt.Errorf("服务器暂时不可用 HTTP %d", r.StatusCode)
			log.Error = last.Error()
			c.record(log)
			continue
		}
		if r.StatusCode != 200 {
			log.Error = "http_error"
			c.record(log)
			return fmt.Errorf("HTTP %d", r.StatusCode)
		}
		var env struct {
			Success     bool            `json:"success"`
			Code        json.RawMessage `json:"code"`
			Description string          `json:"description"`
			Result      json.RawMessage `json:"result"`
			SecretKey   string          `json:"secretKey"`
		}
		if e = json.Unmarshal(b, &env); e != nil {
			log.Error = "non_json_response"
			c.record(log)
			return ErrSession
		}
		log.Code = strings.Trim(string(env.Code), "\"")
		if !env.Success {
			if log.Code == "9" {
				last = fmt.Errorf("接口繁忙 code 9，稍后续采")
				log.Error = "busy"
				c.record(log)
				continue
			}
			log.Error = "api_error"
			c.record(log)
			if strings.Contains(env.Description, "登录") || strings.Contains(env.Description, "验证") || strings.Contains(env.Description, "登陆") {
				return ErrSession
			}
			return fmt.Errorf("接口 %s 失败 code=%s: %s", op, log.Code, env.Description)
		}
		decoded, e := decryptResult(env.Result, env.SecretKey, time.Now())
		if e != nil {
			log.Error = "decode_error"
			c.record(log)
			return e
		}
		if string(decoded) == "null" {
			log.Error = "null_result"
			c.record(log)
			return fmt.Errorf("接口 %s 返回 null", op)
		}
		e = json.Unmarshal(decoded, out)
		if e != nil {
			log.Error = "schema_error"
		}
		c.record(log)
		return e
	}
	return last
}
func (c *Client) record(r RequestLog) {
	if c.Log != nil {
		c.Log(r)
	}
}
func (c *Client) Search(ctx context.Context, q Query, page, size int) (SearchResult, error) {
	var out SearchResult
	q, e := q.Normalize()
	if e != nil {
		return out, e
	}
	if size != 5 && size != 10 && size != 15 {
		return out, fmt.Errorf("page_size 只能为 5、10、15")
	}
	if page < 1 || page*size > 600 {
		return out, fmt.Errorf("页码超出 600 条窗口，请使用采集任务自动拆分")
	}
	var d struct {
		Params *struct {
			Page  int `json:"pageNum"`
			Size  int `json:"pageSize"`
			Items []struct {
				ID    string `json:"id"`
				Value string `json:"value"`
				Oper  string `json:"oper"`
			} `json:"queryItemList"`
		} `json:"queryParams"`
		QR *struct {
			Count *int     `json:"resultCount"`
			List  []Record `json:"resultList"`
		} `json:"queryResult"`
	}
	e = c.call(ctx, "queryDoc", url.Values{"queryCondition": {jsonText(q.Conditions)}, "sortFields": {q.Sort}, "pageNum": {strconv.Itoa(page)}, "pageSize": {strconv.Itoa(size)}}, &d)
	if e != nil {
		return out, e
	}
	if d.QR == nil || d.QR.Count == nil || *d.QR.Count < 0 {
		return out, fmt.Errorf("列表响应缺少有效 resultCount")
	}
	if d.Params != nil {
		if d.Params.Page != page || d.Params.Size != size {
			return out, fmt.Errorf("%w：响应页码/条数不同", ErrQueryMismatch)
		}
		if dateRange := q.Get("cprq"); dateRange != "" {
			bounds := strings.Split(dateRange, " TO ")
			lower, upper := false, false
			for _, item := range d.Params.Items {
				v := strings.ReplaceAll(item.Value, "\\", "")
				if item.ID == "s31" && item.Oper == "GREATER" && v == bounds[0] {
					lower = true
				}
				if item.ID == "s31" && item.Oper == "LESS" && v == bounds[1] {
					upper = true
				}
			}
			if !lower || !upper {
				return out, fmt.Errorf("%w：响应缺少请求的裁判日期范围；请用 page_size=5", ErrQueryMismatch)
			}
		}
	}
	return SearchResult{*d.QR.Count, d.QR.List, page, size}, nil
}
func (c *Client) Facets(ctx context.Context, q Query, key string) ([]Facet, error) {
	q, e := q.Normalize()
	if e != nil {
		return nil, e
	}
	var d map[string][]map[string]any
	e = c.call(ctx, "leftDataItem", url.Values{"queryCondition": {jsonText(q.Conditions)}, "groupFields": {key}}, &d)
	if e != nil {
		return nil, e
	}
	out := []Facet{}
	for _, v := range d[key] {
		n, err := strconv.Atoi(field(v, "count", "valueCount", "docCount"))
		if err != nil {
			return nil, fmt.Errorf("分类计数 %s 缺少 count", key)
		}
		out = append(out, Facet{str(v["value"]), n})
	}
	return out, nil
}
func (c *Client) Detail(ctx context.Context, id string) (Record, error) {
	if strings.TrimSpace(id) == "" {
		return nil, fmt.Errorf("doc_id 不能为空")
	}
	var d Record
	e := c.call(ctx, "docInfoSearch", url.Values{"docId": {id}}, &d)
	if e == nil && field(d, "s1", "s5", "qwContent") == "" {
		e = fmt.Errorf("正文响应缺少文书字段")
	}
	return d, e
}

// CheckSession reads only the website's login endpoint. It never searches or
// fetches a judgment and does not expose account identifiers to callers.
func (c *Client) CheckSession(ctx context.Context) error {
	var user Record
	if e := c.call(ctx, "currentUser", url.Values{}, &user); e != nil {
		return e
	}
	for _, key := range []string{"loginId", "userId", "userName"} {
		v := strings.TrimSpace(field(user, key))
		if v == "" {
			continue
		}
		switch strings.ToLower(v) {
		case "anonymoususer", "anonymous", "匿名用户", "未登录", "未登陆":
			return ErrSession
		}
		return nil
	}
	return ErrSession
}
