package wenshu

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

type Session struct {
	Cookie     string `json:"cookie"`
	Token      string `json:"token,omitempty"`
	UserAgent  string `json:"user_agent,omitempty"`
	ImportedAt string `json:"imported_at"`
}

func readSession(path string) (Session, error) {
	var s Session
	b, e := os.ReadFile(path)
	if e != nil {
		return s, fmt.Errorf("请先 auth-import 或 login: %w", e)
	}
	e = json.Unmarshal(b, &s)
	if e == nil && s.Cookie == "" {
		e = fmt.Errorf("会话没有 cookie")
	}
	return s, e
}
func atomicWrite(path string, b []byte) error {
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".wenshu-*")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if _, e = f.Write(b); e != nil {
		f.Close()
		return e
	}
	if e = f.Sync(); e != nil {
		f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	return os.Rename(f.Name(), path)
}
func SaveSession(path string, s Session) error {
	if strings.TrimSpace(s.Cookie) == "" {
		return fmt.Errorf("会话没有 cookie")
	}
	s.ImportedAt = now()
	return atomicWrite(path, []byte(jsonText(s)))
}

// ImportSession accepts the original probe capture, Playwright storage state,
// a native session JSON, or a plain Cookie header. Credentials never enter logs.
func ImportSession(source, dest string) error {
	b, e := os.ReadFile(source)
	if e != nil {
		return e
	}
	var s Session
	if json.Valid(b) {
		var d struct {
			Session
			Captures []struct {
				URL      string            `json:"url"`
				PostData string            `json:"post_data"`
				Headers  map[string]string `json:"req_headers"`
			} `json:"captures"`
			Cookies json.RawMessage `json:"cookies"`
		}
		if e = json.Unmarshal(b, &d); e != nil {
			return e
		}
		s = d.Session
		for _, c := range d.Captures {
			u, e := url.Parse(c.URL)
			if e != nil || u.Hostname() != "wenshu.court.gov.cn" {
				continue
			}
			p, _ := url.ParseQuery(c.PostData)
			if p.Get("cfg") == "" {
				continue
			}
			for k, v := range c.Headers {
				switch strings.ToLower(k) {
				case "cookie":
					s.Cookie = v
				case "user-agent":
					s.UserAgent = v
				}
			}
			if t := p.Get("__RequestVerificationToken"); t != "" {
				s.Token = t
			}
		}
		// Prefer the most recent exported cookies when the capture has them.
		var cookies []struct {
			Name   string `json:"name"`
			Value  string `json:"value"`
			Domain string `json:"domain"`
			Path   string `json:"path"`
		}
		if json.Unmarshal(d.Cookies, &cookies) == nil && len(cookies) > 0 {
			parts := []string{}
			for _, c := range cookies {
				domain := strings.TrimPrefix(c.Domain, ".")
				if (domain == "wenshu.court.gov.cn" || strings.HasSuffix("wenshu.court.gov.cn", "."+domain)) && (c.Path == "" || strings.HasPrefix("/website/parse/rest.q4w", c.Path)) {
					parts = append(parts, c.Name+"="+c.Value)
				}
			}
			if len(parts) > 0 {
				s.Cookie = strings.Join(parts, "; ")
			}
		}
	} else {
		s.Cookie = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(b)), "Cookie:"))
	}
	return SaveSession(dest, s)
}
