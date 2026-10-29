package wenshu

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// The list rowkey is a request-specific locator. The detail s5 is the stable
// identity confirmed by the server; case numbers and titles are not identities.
const identitySchema = `
CREATE TABLE IF NOT EXISTS doc_aliases(alias TEXT PRIMARY KEY,doc_id TEXT NOT NULL REFERENCES documents(id),metadata TEXT NOT NULL DEFAULT '{}',seen_at TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS alias_document ON doc_aliases(doc_id,seen_at);
CREATE TABLE IF NOT EXISTS coding_merge_history(id INTEGER PRIMARY KEY,original_doc_id TEXT NOT NULL,canonical_doc_id TEXT NOT NULL,coding_json TEXT NOT NULL,merged_at TEXT NOT NULL);
`

func (s *Store) ResolveID(id string) (string, error) {
	var canonical string
	e := s.DB.QueryRow(`SELECT doc_id FROM doc_aliases WHERE alias=?`, id).Scan(&canonical)
	if errors.Is(e, sql.ErrNoRows) {
		return id, nil
	}
	return canonical, e
}
func (s *Store) RemoteID(id string) (string, error) {
	canonical, e := s.ResolveID(id)
	if e != nil {
		return "", e
	}
	var remote string
	e = s.DB.QueryRow(`SELECT alias FROM doc_aliases WHERE doc_id=? ORDER BY seen_at DESC,rowid DESC LIMIT 1`, canonical).Scan(&remote)
	if errors.Is(e, sql.ErrNoRows) {
		return id, nil
	}
	return remote, e
}

func (s *Store) migrateIdentities() error {
	tx, e := s.DB.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	for {
		var id, detail string
		e = tx.QueryRow(`SELECT id,detail FROM documents WHERE json_extract(detail,'$.s5') IS NOT NULL AND json_extract(detail,'$.s5')<>'' AND id<>json_extract(detail,'$.s5') LIMIT 1`).Scan(&id, &detail)
		if errors.Is(e, sql.ErrNoRows) {
			break
		}
		if e != nil {
			return e
		}
		var d Record
		if e = json.Unmarshal([]byte(detail), &d); e != nil {
			return e
		}
		if _, e = mergeIdentity(tx, id, field(d, "s5")); e != nil {
			return e
		}
	}
	if _, e = tx.Exec(`PRAGMA user_version=2`); e != nil {
		return e
	}
	return tx.Commit()
}

func mergeIdentity(tx *sql.Tx, oldID, canonical string) (string, error) {
	if canonical == "" || oldID == canonical {
		return oldID, nil
	}
	var meta, detail, status, body, updated string
	if e := tx.QueryRow(`SELECT metadata,detail,body_status,body_text,updated FROM documents WHERE id=?`, oldID).Scan(&meta, &detail, &status, &body, &updated); e != nil {
		return "", e
	}
	var priorBody, priorStatus, priorUpdated string
	exists := true
	e := tx.QueryRow(`SELECT body_text,body_status,updated FROM documents WHERE id=?`, canonical).Scan(&priorBody, &priorStatus, &priorUpdated)
	if errors.Is(e, sql.ErrNoRows) {
		exists = false
	} else if e != nil {
		return "", e
	}
	useSource := !exists || (status != "pending" && (priorStatus == "pending" || updated >= priorUpdated))
	if !exists {
		if _, e = tx.Exec(`INSERT INTO documents(id,metadata,detail,body_status,body_text,updated) VALUES(?,?,?,?,?,?)`, canonical, meta, detail, status, body, updated); e != nil {
			return "", e
		}
	} else if useSource {
		if priorBody != body {
			if _, e = tx.Exec(`UPDATE codings SET stale=1 WHERE doc_id=?`, canonical); e != nil {
				return "", e
			}
		}
		if _, e = tx.Exec(`UPDATE documents SET metadata=CASE WHEN ?='{}' THEN metadata ELSE ? END,detail=?,body_status=?,body_text=?,updated=? WHERE id=?`, meta, meta, detail, status, body, updated, canonical); e != nil {
			return "", e
		}
	}
	if useSource {
		if _, e = tx.Exec(`DELETE FROM paragraphs WHERE doc_id=?`, canonical); e != nil {
			return "", e
		}
		if _, e = tx.Exec(`INSERT INTO paragraphs(doc_id,number,section,text,start,end) SELECT ?,number,section,text,start,end FROM paragraphs WHERE doc_id=?`, canonical, oldID); e != nil {
			return "", e
		}
	}
	for _, table := range []string{"job_docs", "shard_docs"} {
		key := "job_id"
		if table == "shard_docs" {
			key = "shard_id"
		}
		query := fmt.Sprintf(`INSERT OR IGNORE INTO %s(%s,doc_id) SELECT %s,? FROM %s WHERE doc_id=?`, table, key, key, table)
		if _, e = tx.Exec(query, canonical, oldID); e != nil {
			return "", e
		}
		if _, e = tx.Exec(`DELETE FROM `+table+` WHERE doc_id=?`, oldID); e != nil {
			return "", e
		}
	}
	// Preserve every pre-merge coding, including conflicting assessments by the
	// same coder. The latest assessment remains current, history stays available.
	if _, e = tx.Exec(`INSERT INTO coding_merge_history(original_doc_id,canonical_doc_id,coding_json,merged_at) SELECT doc_id,?,json_object('book',book,'version',version,'variable',variable,'unit',unit,'coder',coder,'value',json(value),'evidence',json(evidence),'source',source,'note',note,'updated_at',updated,'stale',stale),? FROM codings WHERE doc_id=?`, canonical, now(), oldID); e != nil {
		return "", e
	}
	sourceStale := 0
	if !useSource && priorBody != body {
		sourceStale = 1
	}
	if _, e = tx.Exec(`INSERT INTO codings(doc_id,book,version,variable,unit,coder,value,evidence,source,note,updated,stale) SELECT ?,book,version,variable,unit,coder,value,evidence,source,note,updated,max(stale,?) FROM codings WHERE doc_id=? ON CONFLICT(doc_id,book,version,variable,unit,coder) DO UPDATE SET value=excluded.value,evidence=excluded.evidence,source=excluded.source,note=excluded.note,updated=excluded.updated,stale=excluded.stale WHERE excluded.updated>=codings.updated`, canonical, sourceStale, oldID); e != nil {
		return "", e
	}
	if _, e = tx.Exec(`DELETE FROM codings WHERE doc_id=?`, oldID); e != nil {
		return "", e
	}
	if _, e = tx.Exec(`DELETE FROM paragraphs WHERE doc_id=?`, oldID); e != nil {
		return "", e
	}
	if _, e = tx.Exec(`UPDATE doc_aliases SET doc_id=? WHERE doc_id=?`, canonical, oldID); e != nil {
		return "", e
	}
	if _, e = tx.Exec(`INSERT INTO doc_aliases(alias,doc_id,metadata,seen_at) VALUES(?,?,?,?) ON CONFLICT(alias) DO UPDATE SET doc_id=excluded.doc_id,metadata=excluded.metadata,seen_at=excluded.seen_at`, oldID, canonical, meta, updated); e != nil {
		return "", e
	}
	if _, e = tx.Exec(`DELETE FROM documents WHERE id=?`, oldID); e != nil {
		return "", e
	}
	if _, e = tx.Exec(`UPDATE shards SET status='blocked',error='按正文稳定ID归并后，唯一文书数与接口计数不符' WHERE status='completed' AND total<>(SELECT count(*) FROM shard_docs WHERE shard_id=shards.id) AND id IN(SELECT shard_id FROM shard_docs WHERE doc_id=?)`, canonical); e != nil {
		return "", e
	}
	if _, e = tx.Exec(`UPDATE jobs SET status='partial',error='文书身份归并后发现分片覆盖不完整',updated=? WHERE status='completed' AND id IN(SELECT job_id FROM shards WHERE status='blocked')`, now()); e != nil {
		return "", e
	}
	return canonical, nil
}
