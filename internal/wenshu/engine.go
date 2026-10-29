package wenshu

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

var errPaused = errors.New("任务已暂停")

type Runner struct {
	Store *Store
	API   API
}
type RunOptions struct {
	MaxPages          int  `json:"max_pages,omitempty"`
	RequestIntervalMS *int `json:"request_interval_ms,omitempty"`
}

func (r *Runner) Run(ctx context.Context, id string, opt RunOptions) (err error) {
	return r.run(ctx, id, opt, "")
}
func (r *Runner) run(ctx context.Context, id string, opt RunOptions, owner string) (err error) {
	if opt.MaxPages < 0 {
		return fmt.Errorf("max_pages 不能为负数")
	}
	j, e := r.Store.Job(id)
	if e != nil {
		return e
	}
	if j.Status == "completed" {
		return nil
	}
	if owner == "" {
		owner = uuid.NewString()
		if e = r.Store.Acquire(ctx, owner); e != nil {
			return e
		}
	}
	defer r.Store.Release(owner)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	done := make(chan struct{})
	defer close(done)
	go func() {
		t := time.NewTicker(10 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				if e := r.Store.Renew(owner); e != nil {
					cancel(e)
					return
				}
			}
		}
	}()
	if e = r.Store.SetJob(id, "running", ""); e != nil {
		return e
	}
	if e = r.Store.Event(id, "run", "开始/恢复采集"); e != nil {
		return e
	}
	started := time.Now()
	var firstRequest int64
	if e = r.Store.DB.QueryRow(`SELECT coalesce(max(id),0) FROM requests WHERE job_id=?`, id).Scan(&firstRequest); e != nil {
		return e
	}
	defer func() {
		var requests, retries, bodies int
		var networkMS int64
		if e := r.Store.DB.QueryRow(`SELECT count(*),coalesce(sum(attempt>1),0),coalesce(sum(operation='docInfoSearch' AND error='' AND http_status=200),0),coalesce(sum(duration_ms),0) FROM requests WHERE job_id=? AND id>?`, id, firstRequest).Scan(&requests, &retries, &bodies, &networkMS); e == nil {
			elapsed := time.Since(started).Milliseconds()
			var rate float64
			if elapsed > 0 {
				rate = float64(bodies) * 1000 / float64(elapsed)
			}
			stored := 0
			if after, e := r.Store.Job(id); e == nil {
				stored = max(0, after.Bodies-j.Bodies)
			}
			_ = r.Store.Event(id, "run_metrics", jsonText(map[string]any{"elapsed_ms": elapsed, "http_attempts": requests, "retry_attempts": retries, "body_fetches": bodies, "stored_body_increase": stored, "network_ms": networkMS, "body_fetches_per_second": rate, "concurrency": 1, "request_interval_ms": opt.RequestIntervalMS}))
		}
	}()
	defer func() {
		if err != nil {
			state := "error"
			if errors.Is(err, errPaused) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				state = "paused"
			}
			if e := r.Store.SetJob(id, state, err.Error()); e != nil {
				err = errors.Join(err, e)
			}
			_ = r.Store.Event(id, state, err.Error())
		}
	}()
	if _, e = r.Store.DB.Exec(`UPDATE shards SET status=CASE WHEN next_page>1 THEN 'collecting' ELSE 'pending' END,error='' WHERE job_id=? AND status='error'`, id); e != nil {
		return e
	}
	pages := 0
	for {
		if e = r.check(ctx, id); e != nil {
			return e
		}
		if j.Spec.FetchBody {
			if e = r.bodies(ctx, id); e != nil {
				return e
			}
		}
		sh, e := r.Store.NextShard(id)
		if errors.Is(e, sql.ErrNoRows) {
			break
		}
		if e != nil {
			return e
		}
		if opt.MaxPages > 0 && pages >= opt.MaxPages {
			return errPaused
		}
		rs, e := r.API.Search(ctx, sh.Query, sh.NextPage, j.Spec.PageSize)
		if e != nil {
			if errors.Is(e, ErrQueryMismatch) && j.Spec.PageSize != 5 && sh.NextPage == 1 {
				var committed int
				if e2 := r.Store.DB.QueryRow(`SELECT count(*) FROM shards WHERE job_id=? AND next_page>1`, id).Scan(&committed); e2 != nil {
					return e2
				}
				if committed == 0 {
					j.Spec.PageSize = 5
					if _, e2 := r.Store.DB.Exec(`UPDATE jobs SET spec=?,updated=? WHERE id=?`, jsonText(j.Spec), now(), id); e2 != nil {
						return e2
					}
					_ = r.Store.Event(id, "page_size_fallback", "网站未保留日期条件，自动改为每页5条并重试")
					continue
				}
			}
			_ = r.Store.SetShard(sh.ID, "error", sh.Total, e.Error())
			return e
		}
		pages++
		if sh.NextPage > 1 && rs.Total != sh.Total {
			message := fmt.Sprintf("分片计数在分页期间变化：%d → %d；需要重新检索评估覆盖范围", sh.Total, rs.Total)
			if e = r.Store.SetShard(sh.ID, "blocked", rs.Total, message); e != nil {
				return e
			}
			_ = r.Store.Event(id, "count_drift", message)
			continue
		}
		if rs.Total > 600 {
			sh.Total = rs.Total
			if e = r.Store.SetShard(sh.ID, "pending", rs.Total, ""); e != nil {
				return e
			}
			children, why, e := r.partition(ctx, sh, j.Spec.SplitFields)
			if e != nil {
				_ = r.Store.SetShard(sh.ID, "error", sh.Total, e.Error())
				return e
			}
			if len(children) == 0 {
				if e = r.Store.SetShard(sh.ID, "blocked", rs.Total, why); e != nil {
					return e
				}
				_ = r.Store.Event(id, "unsplit", fmt.Sprintf("分片 %d：%s", sh.ID, why))
				continue
			}
			if e = r.Store.Split(sh, children); e != nil {
				return e
			}
			_ = r.Store.Event(id, "split", fmt.Sprintf("分片 %d 共 %d 条，拆成 %d 个子分片；%s", sh.ID, rs.Total, len(children), why))
			continue
		}
		expected := min(j.Spec.PageSize, max(0, rs.Total-(sh.NextPage-1)*j.Spec.PageSize))
		if len(rs.Records) != expected {
			e = fmt.Errorf("分片 %d 第 %d 页应返回 %d 条，实际 %d；断点未前移", sh.ID, sh.NextPage, expected, len(rs.Records))
			_ = r.Store.SetShard(sh.ID, "error", rs.Total, e.Error())
			return e
		}
		if e = r.Store.SavePage(sh, rs); e != nil {
			return e
		}
		if sh.NextPage*j.Spec.PageSize >= rs.Total {
			var unique int
			if e = r.Store.DB.QueryRow(`SELECT count(*) FROM shard_docs WHERE shard_id=?`, sh.ID).Scan(&unique); e != nil {
				return e
			}
			status, message := "completed", ""
			if unique != rs.Total {
				status = "blocked"
				message = fmt.Sprintf("分页去重后 %d 条与接口计数 %d 不符", unique, rs.Total)
			}
			if e = r.Store.SetShard(sh.ID, status, rs.Total, message); e != nil {
				return e
			}
		}
	}
	latest, e := r.Store.Job(id)
	if e != nil {
		return e
	}
	state := "completed"
	message := ""
	if latest.Shards["blocked"] > 0 {
		state = "partial"
		message = "存在无法完整覆盖的分片；详见任务分片和方法附录"
	}
	if e = r.Store.SetJob(id, state, message); e != nil {
		return e
	}
	return r.Store.Event(id, state, fmt.Sprintf("已存文书 %d；正文 %d；未公开正文 %d", latest.Documents, latest.Bodies, latest.Unavailable))
}
func (r *Runner) check(ctx context.Context, id string) error {
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	var state string
	if e := r.Store.DB.QueryRow(`SELECT status FROM jobs WHERE id=?`, id).Scan(&state); e != nil {
		return e
	}
	if state == "pausing" || state == "paused" {
		return errPaused
	}
	return nil
}
func (r *Runner) bodies(ctx context.Context, id string) error {
	for {
		if e := r.check(ctx, id); e != nil {
			return e
		}
		doc, e := r.Store.PendingBody(id)
		if errors.Is(e, sql.ErrNoRows) {
			return nil
		}
		if e != nil {
			return e
		}
		d, e := r.API.Detail(ctx, doc)
		if e != nil {
			return fmt.Errorf("正文 %s: %w", doc, e)
		}
		if e = r.Store.SaveDetail(doc, d); e != nil {
			return e
		}
	}
}
func (r *Runner) partition(ctx context.Context, sh Shard, fields []string) ([]Query, string, error) {
	q := sh.Query
	dateRange := q.Get("cprq")
	if dateRange == "" && q.Get("s42") != "" {
		y, e := strconv.Atoi(q.Get("s42"))
		if e == nil && y >= 1 && y <= 9999 {
			dateRange = fmt.Sprintf("%04d-01-01 TO %04d-12-31", y, y)
		}
	}
	if dateRange != "" {
		p := strings.Split(dateRange, " TO ")
		if len(p) == 2 {
			a, e := time.Parse("2006-01-02", p[0])
			b, e2 := time.Parse("2006-01-02", p[1])
			if e == nil && e2 == nil && a.Before(b) {
				days := int(b.Sub(a).Hours() / 24)
				mid := a.AddDate(0, 0, days/2)
				children := []Query{q.With("cprq", a.Format("2006-01-02")+" TO "+mid.Format("2006-01-02")), q.With("cprq", mid.AddDate(0, 0, 1).Format("2006-01-02")+" TO "+b.Format("2006-01-02"))}
				sum := 0
				for _, c := range children {
					rs, e := r.API.Search(ctx, c, 1, 5)
					if e != nil {
						return nil, "", e
					}
					sum += rs.Total
				}
				if sum == sh.Total {
					return children, "不重叠的裁判日期区间", nil
				}
				_ = r.Store.Event(sh.JobID, "split_mismatch", fmt.Sprintf("分片 %d 日期子集计数 %d 不等于父集 %d", sh.ID, sum, sh.Total))
			}
		}
	}
	for _, k := range fields {
		if q.Get(k) != "" {
			continue
		}
		if dateRange != "" && k == "s42" {
			continue
		}
		fs, e := r.API.Facets(ctx, q, k)
		if e != nil {
			return nil, "", e
		}
		sum := 0
		seen := map[string]bool{}
		valid := len(fs) > 0
		for _, f := range fs {
			if f.Count <= 0 {
				continue
			}
			if f.Value == "" || seen[f.Value] || f.Count > sh.Total {
				valid = false
				break
			}
			seen[f.Value] = true
			sum += f.Count
		}
		if !valid || sum != sh.Total {
			_ = r.Store.Event(sh.JobID, "split_mismatch", fmt.Sprintf("分片 %d 字段 %s 分组计数 %d，父集 %d；未采用", sh.ID, k, sum, sh.Total))
			continue
		}
		sort.Slice(fs, func(i, j int) bool { return fs[i].Value < fs[j].Value })
		children := []Query{}
		verified := 0
		for _, f := range fs {
			if f.Count <= 0 {
				continue
			}
			child := q.With(k, f.Value)
			rs, e := r.API.Search(ctx, child, 1, 5)
			if e != nil {
				return nil, "", e
			}
			if rs.Total != f.Count {
				valid = false
				break
			}
			verified += rs.Total
			children = append(children, child)
		}
		if valid && verified == sh.Total && len(children) > 0 {
			return children, "互斥分类 " + k + "，逐项校对计数", nil
		}
	}
	return nil, "所有可用日期/互斥分类均不能覆盖拆分；保留该分片，未把前 600 条当作全集", nil
}

type Manager struct {
	Store       *Store
	SessionPath string
	Gap         time.Duration
	mu          sync.Mutex
	cancel      context.CancelFunc
	active      string
	wg          sync.WaitGroup
}

func (m *Manager) Start(parent context.Context, id string, opt RunOptions) (Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active != "" {
		return Job{}, fmt.Errorf("任务 %s 正在运行", m.active)
	}
	j, e := m.Store.Job(id)
	if e != nil {
		return j, e
	}
	if j.Status == "completed" {
		return j, nil
	}
	if opt.MaxPages < 0 {
		return j, fmt.Errorf("max_pages 不能为负数")
	}
	if opt.RequestIntervalMS != nil && *opt.RequestIntervalMS < 0 {
		return j, fmt.Errorf("request_interval_ms 不能为负数")
	}
	owner := uuid.NewString()
	if e = m.Store.Acquire(parent, owner); e != nil {
		return j, e
	}
	ctx, cancel := context.WithCancel(parent)
	m.cancel = cancel
	m.active = id
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer m.Store.Release(owner)
		c := NewClient(m.SessionPath)
		c.Gap = m.Gap
		if opt.RequestIntervalMS != nil {
			c.Gap = time.Duration(*opt.RequestIntervalMS) * time.Millisecond
		}
		actualInterval := int(c.Gap / time.Millisecond)
		opt.RequestIntervalMS = &actualInterval
		c.Log = func(x RequestLog) { m.Store.LogRequest(id, x) }
		r := Runner{m.Store, c}
		_ = r.run(ctx, id, opt, owner)
		m.mu.Lock()
		m.active = ""
		m.cancel = nil
		m.mu.Unlock()
	}()
	return j, nil
}
func (m *Manager) Pause(id string) error {
	j, e := m.Store.Job(id)
	if e != nil {
		return e
	}
	if j.Status == "completed" {
		return nil
	}
	state := "paused"
	if j.Status == "running" {
		state = "pausing"
	}
	if e = m.Store.SetJob(id, state, "用户请求暂停"); e != nil {
		return e
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active == id && m.cancel != nil {
		m.cancel()
	}
	return nil
}
func (m *Manager) Close() {
	m.mu.Lock()
	if m.cancel != nil {
		m.cancel()
	}
	m.mu.Unlock()
	m.wg.Wait()
}
