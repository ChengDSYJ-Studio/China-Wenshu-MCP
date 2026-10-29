package wenshu

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
)

type LoginStatus struct {
	State   string `json:"state"`
	Message string `json:"message"`
}
type LoginManager struct {
	mu     sync.Mutex
	status LoginStatus
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func (l *LoginManager) Status() LoginStatus { l.mu.Lock(); defer l.mu.Unlock(); return l.status }
func (l *LoginManager) Start(parent context.Context, session, profile, chrome string) (LoginStatus, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cancel != nil {
		return l.status, nil
	}
	bin, e := ResolveBrowser(chrome, filepath.Dir(profile))
	if e != nil {
		return LoginStatus{}, e
	}
	ctx, cancel := context.WithTimeout(parent, 15*time.Minute)
	l.cancel = cancel
	l.status = LoginStatus{"waiting", "请在浏览器中完成登录，程序会自动保存会话"}
	l.wg.Add(1)
	go func() {
		defer l.wg.Done()
		e := login(ctx, session, profile, bin)
		cancel()
		l.mu.Lock()
		defer l.mu.Unlock()
		l.cancel = nil
		if e != nil {
			l.status = LoginStatus{"error", e.Error()}
		} else {
			l.status = LoginStatus{"ready", "登录成功，会话已保存"}
		}
	}()
	return l.status, nil
}
func (l *LoginManager) Close() {
	l.mu.Lock()
	if l.cancel != nil {
		l.cancel()
	}
	l.mu.Unlock()
	l.wg.Wait()
}
func login(ctx context.Context, session, profile, chrome string) error {
	launch := launcher.New().Context(ctx).Bin(chrome).Headless(false).UserDataDir(profile)
	control, e := launch.Launch()
	if e != nil {
		return e
	}
	defer launch.Kill()
	browser := rod.New().ControlURL(control).Context(ctx)
	if e = browser.Connect(); e != nil {
		return e
	}
	defer browser.Close()
	page, e := browser.Page(proto.TargetCreateTarget{URL: "about:blank"})
	if e != nil {
		return e
	}
	if e = page.Navigate("https://wenshu.court.gov.cn/"); e != nil {
		return e
	}
	candidateDir, e := os.MkdirTemp(filepath.Dir(session), ".login-")
	if e != nil {
		return e
	}
	defer os.RemoveAll(candidateDir)
	candidate := filepath.Join(candidateDir, "session.json")
	client := NewClient(candidate)
	client.Retries = 0
	client.HTTP.Timeout = 10 * time.Second
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			cookies, e := browser.GetCookies()
			if e != nil {
				return e
			}
			parts := []string{}
			for _, c := range cookies {
				domain := strings.TrimPrefix(c.Domain, ".")
				if (domain == "wenshu.court.gov.cn" || strings.HasSuffix("wenshu.court.gov.cn", "."+domain)) && strings.HasPrefix("/website/parse/rest.q4w", c.Path) {
					parts = append(parts, c.Name+"="+c.Value)
				}
			}
			if len(parts) == 0 {
				continue
			}
			s := Session{Cookie: strings.Join(parts, "; ")}
			if v, e := page.Eval(`() => navigator.userAgent`); e == nil {
				s.UserAgent = v.Value.Str()
			}
			if e = SaveSession(candidate, s); e != nil {
				return e
			}
			e = client.CheckSession(ctx)
			if e == nil {
				return SaveSession(session, s)
			}
		}
	}
}
