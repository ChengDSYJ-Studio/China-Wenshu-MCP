package wenshu

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

type Store struct {
	DB   *sql.DB
	Path string
}

func OpenStore(path string) (*Store, error) {
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return nil, e
	}
	db, e := sql.Open("sqlite", path)
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(1)
	s := &Store{db, path}
	if _, e = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON; PRAGMA busy_timeout=10000;`); e != nil {
		db.Close()
		return nil, e
	}
	var version int
	if e = db.QueryRow(`PRAGMA user_version`).Scan(&version); e != nil {
		db.Close()
		return nil, e
	}
	if version > 2 {
		db.Close()
		return nil, fmt.Errorf("语料库版本 %d 高于程序支持版本", version)
	}
	if _, e = db.Exec(schema); e != nil {
		db.Close()
		return nil, e
	}
	if _, e = db.Exec(identitySchema); e != nil {
		db.Close()
		return nil, e
	}
	if version < 2 {
		if e = s.migrateIdentities(); e != nil {
			db.Close()
			return nil, e
		}
	}
	return s, nil
}
func (s *Store) Close() error { return s.DB.Close() }

const schema = `
CREATE TABLE IF NOT EXISTS jobs(id TEXT PRIMARY KEY,spec TEXT NOT NULL,status TEXT NOT NULL,error TEXT NOT NULL DEFAULT '',created TEXT NOT NULL,updated TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS shards(id INTEGER PRIMARY KEY,job_id TEXT NOT NULL REFERENCES jobs(id),parent INTEGER NOT NULL DEFAULT 0,query TEXT NOT NULL,status TEXT NOT NULL DEFAULT 'pending',total INTEGER NOT NULL DEFAULT -1,next_page INTEGER NOT NULL DEFAULT 1,error TEXT NOT NULL DEFAULT '');
CREATE INDEX IF NOT EXISTS shard_job ON shards(job_id,status);
CREATE TABLE IF NOT EXISTS documents(id TEXT PRIMARY KEY,metadata TEXT NOT NULL,detail TEXT NOT NULL DEFAULT '{}',body_status TEXT NOT NULL DEFAULT 'pending',body_text TEXT NOT NULL DEFAULT '',updated TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS job_docs(job_id TEXT NOT NULL REFERENCES jobs(id),doc_id TEXT NOT NULL REFERENCES documents(id),PRIMARY KEY(job_id,doc_id));
CREATE TABLE IF NOT EXISTS shard_docs(shard_id INTEGER NOT NULL REFERENCES shards(id),doc_id TEXT NOT NULL REFERENCES documents(id),PRIMARY KEY(shard_id,doc_id));
CREATE TABLE IF NOT EXISTS paragraphs(doc_id TEXT NOT NULL REFERENCES documents(id),number INTEGER NOT NULL,section TEXT NOT NULL,text TEXT NOT NULL,start INTEGER NOT NULL,end INTEGER NOT NULL,PRIMARY KEY(doc_id,number));
CREATE TABLE IF NOT EXISTS events(id INTEGER PRIMARY KEY,job_id TEXT NOT NULL,kind TEXT NOT NULL,message TEXT NOT NULL,at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS requests(id INTEGER PRIMARY KEY,job_id TEXT NOT NULL,operation TEXT NOT NULL,http_status INTEGER NOT NULL,code TEXT NOT NULL,attempt INTEGER NOT NULL,duration_ms INTEGER NOT NULL,error TEXT NOT NULL,at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS leases(name TEXT PRIMARY KEY,owner TEXT NOT NULL,expires INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS codebooks(name TEXT NOT NULL,version TEXT NOT NULL,body TEXT NOT NULL,created TEXT NOT NULL,PRIMARY KEY(name,version));
CREATE TABLE IF NOT EXISTS codings(doc_id TEXT NOT NULL REFERENCES documents(id),book TEXT NOT NULL,version TEXT NOT NULL,variable TEXT NOT NULL,unit INTEGER NOT NULL,coder TEXT NOT NULL,value TEXT NOT NULL,evidence TEXT NOT NULL,source TEXT NOT NULL,note TEXT NOT NULL,updated TEXT NOT NULL,stale INTEGER NOT NULL DEFAULT 0,PRIMARY KEY(doc_id,book,version,variable,unit,coder),FOREIGN KEY(book,version) REFERENCES codebooks(name,version));
`

func (s *Store) CreateJob(spec JobSpec) (Job, error) {
	var j Job
	q, e := spec.Query.Normalize()
	if e != nil {
		return j, e
	}
	spec.Query = q
	if spec.PageSize == 0 {
		spec.PageSize = 5
	}
	if spec.PageSize != 5 && spec.PageSize != 10 && spec.PageSize != 15 {
		return j, fmt.Errorf("page_size 只能为 5、10、15")
	}
	if len(spec.SplitFields) == 0 {
		spec.SplitFields = []string{"s42", "s33", "s39", "s40", "s8", "s6", "s4", "s9", "s10"}
	}
	for _, k := range spec.SplitFields {
		switch k {
		case "s42", "s33", "s39", "s40", "s8", "s6", "s4", "s9", "s10":
		default:
			return j, fmt.Errorf("拆分字段 %s 未被定义为互斥分类", k)
		}
	}
	j = Job{ID: uuid.NewString(), Spec: spec, Status: "created", Created: now(), Updated: now()}
	tx, e := s.DB.Begin()
	if e != nil {
		return j, e
	}
	defer tx.Rollback()
	if _, e = tx.Exec(`INSERT INTO jobs(id,spec,status,created,updated) VALUES(?,?,?,?,?)`, j.ID, jsonText(spec), j.Status, j.Created, j.Updated); e != nil {
		return j, e
	}
	if _, e = tx.Exec(`INSERT INTO shards(job_id,query) VALUES(?,?)`, j.ID, jsonText(q)); e != nil {
		return j, e
	}
	if e = tx.Commit(); e != nil {
		return j, e
	}
	return s.Job(j.ID)
}
func (s *Store) Job(id string) (Job, error) {
	var j Job
	var spec string
	e := s.DB.QueryRow(`SELECT id,spec,status,error,created,updated FROM jobs WHERE id=?`, id).Scan(&j.ID, &spec, &j.Status, &j.Error, &j.Created, &j.Updated)
	if e != nil {
		return j, e
	}
	if e = json.Unmarshal([]byte(spec), &j.Spec); e != nil {
		return j, e
	}
	e = s.DB.QueryRow(`SELECT count(*),coalesce(sum(d.body_status='available'),0),coalesce(sum(d.body_status='unavailable'),0) FROM job_docs jd JOIN documents d ON d.id=jd.doc_id WHERE jd.job_id=?`, id).Scan(&j.Documents, &j.Bodies, &j.Unavailable)
	if e != nil {
		return j, e
	}
	rows, e := s.DB.Query(`SELECT status,count(*) FROM shards WHERE job_id=? GROUP BY status`, id)
	if e != nil {
		return j, e
	}
	defer rows.Close()
	j.Shards = map[string]int{}
	for rows.Next() {
		var k string
		var n int
		if e = rows.Scan(&k, &n); e != nil {
			return j, e
		}
		j.Shards[k] = n
	}
	return j, rows.Err()
}
func (s *Store) Jobs() (out []Job, err error) {
	out = []Job{}
	rows, e := s.DB.Query(`SELECT id FROM jobs ORDER BY created DESC`)
	if e != nil {
		return nil, e
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return nil, e
		}
		ids = append(ids, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	for _, id := range ids {
		j, e := s.Job(id)
		if e != nil {
			return nil, e
		}
		out = append(out, j)
	}
	return out, nil
}
func (s *Store) SetJob(id, status, message string) error {
	r, e := s.DB.Exec(`UPDATE jobs SET status=?,error=?,updated=? WHERE id=?`, status, message, now(), id)
	if e != nil {
		return e
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}
func (s *Store) Event(job, kind, message string) error {
	_, e := s.DB.Exec(`INSERT INTO events(job_id,kind,message,at) VALUES(?,?,?,?)`, job, kind, message, now())
	return e
}
func (s *Store) LogRequest(job string, r RequestLog) {
	_, _ = s.DB.Exec(`INSERT INTO requests(job_id,operation,http_status,code,attempt,duration_ms,error,at) VALUES(?,?,?,?,?,?,?,?)`, job, r.Operation, r.Status, r.Code, r.Attempt, r.DurationMS, r.Error, now())
}
func scanShard(row interface{ Scan(...any) error }) (Shard, error) {
	var x Shard
	var q string
	e := row.Scan(&x.ID, &x.JobID, &x.Parent, &q, &x.Status, &x.Total, &x.NextPage, &x.Error)
	if e == nil {
		e = json.Unmarshal([]byte(q), &x.Query)
	}
	return x, e
}
func (s *Store) Shards(id string) ([]Shard, error) {
	rows, e := s.DB.Query(`SELECT id,job_id,parent,query,status,total,next_page,error FROM shards WHERE job_id=? ORDER BY id`, id)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Shard{}
	for rows.Next() {
		x, e := scanShard(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, x)
	}
	return out, rows.Err()
}
func (s *Store) NextShard(job string) (Shard, error) {
	return scanShard(s.DB.QueryRow(`SELECT id,job_id,parent,query,status,total,next_page,error FROM shards WHERE job_id=? AND status IN ('pending','collecting') ORDER BY id LIMIT 1`, job))
}
func (s *Store) SetShard(id int64, status string, total int, message string) error {
	_, e := s.DB.Exec(`UPDATE shards SET status=?,total=?,error=? WHERE id=?`, status, total, message, id)
	return e
}
func (s *Store) Split(parent Shard, children []Query) error {
	tx, e := s.DB.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	for _, q := range children {
		if _, e = tx.Exec(`INSERT INTO shards(job_id,parent,query) VALUES(?,?,?)`, parent.JobID, parent.ID, jsonText(q)); e != nil {
			return e
		}
	}
	if _, e = tx.Exec(`UPDATE shards SET status='split',error='' WHERE id=?`, parent.ID); e != nil {
		return e
	}
	return tx.Commit()
}
func (s *Store) SavePage(sh Shard, rs SearchResult) error {
	tx, e := s.DB.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	for _, m := range rs.Records {
		id := field(m, "rowkey", "s5", "docId")
		if id == "" {
			return fmt.Errorf("列表记录缺少文书 ID")
		}
		var known string
		if err := tx.QueryRow(`SELECT doc_id FROM doc_aliases WHERE alias=?`, id).Scan(&known); err == nil {
			id = known
		} else if err != sql.ErrNoRows {
			return err
		}
		if _, e = tx.Exec(`INSERT INTO documents(id,metadata,updated) VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET metadata=excluded.metadata,updated=excluded.updated`, id, jsonText(m), now()); e != nil {
			return e
		}
		if _, e = tx.Exec(`INSERT OR IGNORE INTO job_docs(job_id,doc_id) VALUES(?,?)`, sh.JobID, id); e != nil {
			return e
		}
		if _, e = tx.Exec(`INSERT OR IGNORE INTO shard_docs(shard_id,doc_id) VALUES(?,?)`, sh.ID, id); e != nil {
			return e
		}
	}
	if _, e = tx.Exec(`UPDATE shards SET next_page=?,total=?,status='collecting' WHERE id=?`, rs.Page+1, rs.Total, sh.ID); e != nil {
		return e
	}
	return tx.Commit()
}
func (s *Store) SaveDetail(id string, d Record) error {
	remoteID := id
	resolved, e := s.ResolveID(id)
	if e != nil {
		return e
	}
	id = resolved
	text, ps := ExtractParagraphs(d)
	status := "available"
	if text == "" {
		if field(d, "s43") == "02" || field(d, "s32") != "" {
			status = "unavailable"
		} else {
			return fmt.Errorf("正文为空且没有不公开标记，保留断点等待重试")
		}
	}
	tx, e := s.DB.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.Exec(`UPDATE codings SET stale=1 WHERE doc_id=? AND EXISTS(SELECT 1 FROM documents WHERE id=? AND body_text<>?)`, id, id, text); e != nil {
		return e
	}
	_, e = tx.Exec(`INSERT INTO documents(id,metadata,detail,body_status,body_text,updated) VALUES(?,'{}',?,?,?,?) ON CONFLICT(id) DO UPDATE SET detail=excluded.detail,body_status=excluded.body_status,body_text=excluded.body_text,updated=excluded.updated`, id, jsonText(d), status, text, now())
	if e != nil {
		return e
	}
	if _, e = tx.Exec(`DELETE FROM paragraphs WHERE doc_id=?`, id); e != nil {
		return e
	}
	for _, p := range ps {
		_, e = tx.Exec(`INSERT INTO paragraphs(doc_id,number,section,text,start,end) VALUES(?,?,?,?,?,?)`, id, p.Number, p.Section, p.Text, p.Start, p.End)
		if e != nil {
			return e
		}
	}
	canonical, e := mergeIdentity(tx, id, field(d, "s5"))
	if e != nil {
		return e
	}
	if remoteID != canonical {
		if _, e = tx.Exec(`INSERT INTO doc_aliases(alias,doc_id,seen_at) VALUES(?,?,?) ON CONFLICT(alias) DO UPDATE SET seen_at=excluded.seen_at`, remoteID, canonical, now()); e != nil {
			return e
		}
	}
	return tx.Commit()
}
func (s *Store) PendingBody(job string) (string, error) {
	var id string
	e := s.DB.QueryRow(`SELECT d.id FROM documents d JOIN job_docs j ON d.id=j.doc_id WHERE j.job_id=? AND d.body_status='pending' ORDER BY d.id LIMIT 1`, job).Scan(&id)
	return id, e
}
func (s *Store) Document(id string) (Document, error) {
	var d Document
	resolved, e := s.ResolveID(id)
	if e != nil {
		return d, e
	}
	id = resolved
	var meta, detail string
	e = s.DB.QueryRow(`SELECT id,metadata,detail,body_status,body_text,updated FROM documents WHERE id=?`, id).Scan(&d.ID, &meta, &detail, &d.BodyStatus, &d.Text, &d.Updated)
	if e != nil {
		return d, e
	}
	if e = json.Unmarshal([]byte(meta), &d.Metadata); e != nil {
		return d, e
	}
	if e = json.Unmarshal([]byte(detail), &d.Detail); e != nil {
		return d, e
	}
	pick := func(keys ...string) string {
		if v := field(d.Detail, keys...); v != "" {
			return v
		}
		return field(d.Metadata, keys...)
	}
	d.Title = strings.Join(plainLines(pick("s1", "1")), " ")
	d.Court = pick("s2", "2")
	d.CaseNumber = pick("s7", "7")
	d.JudgmentDate = pick("s31", "31")
	d.CaseType = pick("s8", "8")
	d.DocType = pick("s6", "6")
	d.RemoteID, e = s.RemoteID(id)
	if e != nil {
		return d, e
	}
	rows, e := s.DB.Query(`SELECT number,section,text,start,end FROM paragraphs WHERE doc_id=? ORDER BY number`, id)
	if e != nil {
		return d, e
	}
	defer rows.Close()
	d.Paragraphs = []Paragraph{}
	for rows.Next() {
		var p Paragraph
		if e = rows.Scan(&p.Number, &p.Section, &p.Text, &p.Start, &p.End); e != nil {
			return d, e
		}
		d.Paragraphs = append(d.Paragraphs, p)
	}
	return d, rows.Err()
}
func (s *Store) DocumentIDs(job, keyword string, limit, offset int) ([]string, error) {
	if limit < 1 || limit > 10000 {
		return nil, fmt.Errorf("limit 必须在 1..10000")
	}
	if offset < 0 {
		return nil, fmt.Errorf("offset 不能为负数")
	}
	rows, e := s.DB.Query(`SELECT d.id FROM documents d WHERE (?='' OR EXISTS(SELECT 1 FROM job_docs j WHERE j.doc_id=d.id AND j.job_id=?)) AND (?='' OR instr(d.body_text,?)>0 OR instr(d.metadata,?)>0 OR instr(d.detail,?)>0) ORDER BY d.id LIMIT ? OFFSET ?`, job, job, keyword, keyword, keyword, keyword, limit, offset)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			return nil, e
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
func (s *Store) Corpus(job, keyword string, limit, offset int) ([]Document, error) {
	ids, e := s.DocumentIDs(job, keyword, limit, offset)
	if e != nil {
		return nil, e
	}
	out := []Document{}
	for _, id := range ids {
		d, e := s.Document(id)
		if e != nil {
			return nil, e
		}
		d.Text = ""
		d.Paragraphs = nil
		d.Detail = nil
		out = append(out, d)
	}
	return out, nil
}
func (s *Store) Acquire(ctx context.Context, owner string) error {
	r, e := s.DB.ExecContext(ctx, `INSERT INTO leases(name,owner,expires) VALUES('collector',?,unixepoch()+120) ON CONFLICT(name) DO UPDATE SET owner=excluded.owner,expires=excluded.expires WHERE leases.expires<unixepoch()`, owner)
	if e != nil {
		return e
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		return fmt.Errorf("已有采集进程运行；请等待、暂停该任务，或在异常退出后等待 120 秒")
	}
	return nil
}
func (s *Store) Renew(owner string) error {
	r, e := s.DB.Exec(`UPDATE leases SET expires=unixepoch()+120 WHERE name='collector' AND owner=?`, owner)
	if e != nil {
		return e
	}
	n, _ := r.RowsAffected()
	if n != 1 {
		return fmt.Errorf("采集锁已失效")
	}
	return nil
}
func (s *Store) Release(owner string) {
	_, _ = s.DB.Exec(`DELETE FROM leases WHERE name='collector' AND owner=?`, owner)
}
