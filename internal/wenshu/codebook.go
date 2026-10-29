package wenshu

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

type Rule struct {
	Pattern  string   `json:"pattern" yaml:"pattern"`
	Value    any      `json:"value" yaml:"value"`
	Sections []string `json:"sections,omitempty" yaml:"sections,omitempty"`
}
type Variable struct {
	Name        string            `json:"name" yaml:"name"`
	Label       string            `json:"label" yaml:"label"`
	Type        string            `json:"type" yaml:"type"`
	Unit        string            `json:"unit" yaml:"unit"`
	Description string            `json:"description,omitempty" yaml:"description,omitempty"`
	Values      map[string]string `json:"values,omitempty" yaml:"values,omitempty"`
	Rules       []Rule            `json:"rules,omitempty" yaml:"rules,omitempty"`
	Default     any               `json:"default,omitempty" yaml:"default,omitempty"`
}
type Codebook struct {
	Name        string     `json:"name" yaml:"name"`
	Version     string     `json:"version" yaml:"version"`
	Description string     `json:"description,omitempty" yaml:"description,omitempty"`
	Variables   []Variable `json:"variables" yaml:"variables"`
}
type Evidence struct {
	Paragraph int    `json:"paragraph"`
	Quote     string `json:"quote"`
}
type Coding struct {
	DocID    string     `json:"doc_id"`
	Book     string     `json:"book"`
	Version  string     `json:"version"`
	Variable string     `json:"variable"`
	Unit     int        `json:"unit"`
	Coder    string     `json:"coder"`
	Value    any        `json:"value"`
	Evidence []Evidence `json:"evidence,omitempty"`
	Source   string     `json:"source,omitempty"`
	Note     string     `json:"note,omitempty"`
	Updated  string     `json:"updated_at,omitempty"`
	Stale    bool       `json:"stale,omitempty"`
}

var varName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,31}$`)

func (v Variable) validateValue(value any) error {
	if value == nil {
		return nil
	}
	switch v.Type {
	case "string":
		if _, ok := value.(string); !ok {
			return fmt.Errorf("%s 需要字符串", v.Name)
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("%s 需要布尔值", v.Name)
		}
	case "number", "integer":
		f, e := asNumber(value)
		if e != nil || math.IsNaN(f) || math.IsInf(f, 0) || v.Type == "integer" && math.Trunc(f) != f {
			return fmt.Errorf("%s 需要有效%s值", v.Name, v.Type)
		}
	case "category":
		if _, ok := v.Values[str(value)]; !ok {
			return fmt.Errorf("%s 的取值 %v 不在 values 中", v.Name, value)
		}
	default:
		return fmt.Errorf("未知变量类型 %s", v.Type)
	}
	return nil
}
func asNumber(x any) (float64, error) {
	switch n := x.(type) {
	case float64:
		return n, nil
	case float32:
		return float64(n), nil
	case int:
		return float64(n), nil
	case int64:
		return float64(n), nil
	case json.Number:
		return n.Float64()
	default:
		return 0, fmt.Errorf("不是数值")
	}
}
func (b Codebook) Validate() error {
	if b.Name == "" || b.Version == "" || len(b.Variables) == 0 {
		return fmt.Errorf("codebook 必须有 name、version、variables")
	}
	seen := map[string]bool{}
	for _, v := range b.Variables {
		if !varName.MatchString(v.Name) || seen[v.Name] {
			return fmt.Errorf("变量名 %q 重复或不符合 Stata 标识符要求", v.Name)
		}
		seen[v.Name] = true
		switch v.Type {
		case "string", "boolean", "integer", "number", "category":
		default:
			return fmt.Errorf("变量 %s 类型不支持", v.Name)
		}
		if v.Unit != "document" && v.Unit != "paragraph" {
			return fmt.Errorf("变量 %s 的 unit 必须为 document 或 paragraph", v.Name)
		}
		if v.Type == "category" && len(v.Values) == 0 {
			return fmt.Errorf("分类变量 %s 缺少 values", v.Name)
		}
		if e := v.validateValue(v.Default); e != nil {
			return e
		}
		for _, r := range v.Rules {
			if r.Pattern == "" {
				return fmt.Errorf("%s 存在空正则", v.Name)
			}
			if _, e := regexp.Compile(r.Pattern); e != nil {
				return e
			}
			if e := v.validateValue(r.Value); e != nil {
				return e
			}
		}
	}
	return nil
}
func (s *Store) ImportCodebook(path string) (Codebook, error) {
	var b Codebook
	data, e := os.ReadFile(path)
	if e != nil {
		return b, e
	}
	if json.Valid(data) {
		dec := json.NewDecoder(strings.NewReader(string(data)))
		dec.DisallowUnknownFields()
		e = dec.Decode(&b)
	} else {
		dec := yaml.NewDecoder(strings.NewReader(string(data)))
		dec.KnownFields(true)
		e = dec.Decode(&b)
	}
	if e != nil {
		return b, e
	}
	if e = b.Validate(); e != nil {
		return b, e
	}
	canonical := jsonText(b)
	var old string
	e = s.DB.QueryRow(`SELECT body FROM codebooks WHERE name=? AND version=?`, b.Name, b.Version).Scan(&old)
	if e == nil {
		if old != canonical {
			return b, fmt.Errorf("同名同版本 codebook 已存在且内容不同，请提升 version")
		}
		return b, nil
	}
	if e != sql.ErrNoRows {
		return b, e
	}
	_, e = s.DB.Exec(`INSERT INTO codebooks(name,version,body,created) VALUES(?,?,?,?)`, b.Name, b.Version, canonical, now())
	return b, e
}
func (s *Store) Codebook(name, version string) (Codebook, error) {
	var b Codebook
	var data string
	e := s.DB.QueryRow(`SELECT body FROM codebooks WHERE name=? AND version=?`, name, version).Scan(&data)
	if e == nil {
		e = json.Unmarshal([]byte(data), &b)
	}
	return b, e
}
func (s *Store) Codebooks() ([]Codebook, error) {
	rows, e := s.DB.Query(`SELECT body FROM codebooks ORDER BY name,version`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Codebook{}
	for rows.Next() {
		var data string
		if e = rows.Scan(&data); e != nil {
			return nil, e
		}
		var b Codebook
		if e = json.Unmarshal([]byte(data), &b); e != nil {
			return nil, e
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
func (s *Store) PutCoding(c Coding) error {
	b, e := s.Codebook(c.Book, c.Version)
	if e != nil {
		return e
	}
	var variable *Variable
	for _, v := range b.Variables {
		if v.Name == c.Variable {
			x := v
			variable = &x
			break
		}
	}
	if variable == nil {
		return fmt.Errorf("codebook 没有变量 %s", c.Variable)
	}
	// Codex exposes an unconstrained JSON value as a string input. Decode that
	// transport representation using the declared codebook type, not heuristics.
	if raw, ok := c.Value.(string); ok && (variable.Type == "boolean" || variable.Type == "integer" || variable.Type == "number") {
		var typed any
		dec := json.NewDecoder(strings.NewReader(raw))
		dec.UseNumber()
		if e := dec.Decode(&typed); e != nil {
			return fmt.Errorf("%s 的 value 无法按 %s 解析", c.Variable, variable.Type)
		}
		if !json.Valid([]byte(raw)) {
			return fmt.Errorf("%s 的 value 不是有效 JSON 值", c.Variable)
		}
		c.Value = typed
	}
	if e = variable.validateValue(c.Value); e != nil {
		return e
	}
	if strings.TrimSpace(c.Coder) == "" {
		return fmt.Errorf("coder 不能为空")
	}
	if c.Source == "" {
		c.Source = "manual"
	}
	if c.Source != "manual" && c.Source != "rule" {
		return fmt.Errorf("source 必须为 manual 或 rule")
	}
	d, e := s.Document(c.DocID)
	if e != nil {
		return e
	}
	c.DocID = d.ID
	if d.BodyStatus != "available" {
		return fmt.Errorf("文书正文尚不可用")
	}
	if variable.Unit == "document" && c.Unit != 0 || variable.Unit == "paragraph" && (c.Unit < 1 || c.Unit > len(d.Paragraphs)) {
		return fmt.Errorf("unit 与编码单位不符；文书=0，段落=段落号")
	}
	for _, ev := range c.Evidence {
		if ev.Paragraph < 1 || ev.Paragraph > len(d.Paragraphs) || ev.Quote == "" || !strings.Contains(d.Paragraphs[ev.Paragraph-1].Text, ev.Quote) {
			return fmt.Errorf("证据不属于指定段落")
		}
		if c.Unit > 0 && c.Unit != ev.Paragraph {
			return fmt.Errorf("段落编码的证据必须来自该段落")
		}
	}
	if len(c.Evidence) == 0 && strings.TrimSpace(c.Note) == "" {
		return fmt.Errorf("请提供原文 evidence 或缺失/阴性编码 note")
	}
	return s.writeCoding(c)
}
func (s *Store) writeCoding(c Coding) error {
	c.Updated = now()
	_, e := s.DB.Exec(`INSERT INTO codings(doc_id,book,version,variable,unit,coder,value,evidence,source,note,updated,stale) VALUES(?,?,?,?,?,?,?,?,?,?,?,0) ON CONFLICT(doc_id,book,version,variable,unit,coder) DO UPDATE SET value=excluded.value,evidence=excluded.evidence,source=excluded.source,note=excluded.note,updated=excluded.updated,stale=0`, c.DocID, c.Book, c.Version, c.Variable, c.Unit, c.Coder, jsonText(c.Value), jsonText(c.Evidence), c.Source, c.Note, c.Updated)
	return e
}
func (s *Store) Codings(job, book, version, coder string) ([]Coding, error) {
	return s.CodingPage(job, book, version, coder, -1, 0)
}
func (s *Store) CodingPage(job, book, version, coder string, limit, offset int) ([]Coding, error) {
	out := []Coding{}
	e := s.eachCoding(job, book, version, coder, limit, offset, func(c Coding) error { out = append(out, c); return nil })
	return out, e
}
func (s *Store) eachCoding(job, book, version, coder string, limit, offset int, fn func(Coding) error) error {
	rows, e := s.DB.Query(`SELECT doc_id,book,version,variable,unit,coder,value,evidence,source,note,updated,stale FROM codings c WHERE (?='' OR EXISTS(SELECT 1 FROM job_docs j WHERE j.job_id=? AND j.doc_id=c.doc_id)) AND (?='' OR book=?) AND (?='' OR version=?) AND (?='' OR coder=?) ORDER BY doc_id,book,version,variable,unit,coder LIMIT ? OFFSET ?`, job, job, book, book, version, version, coder, coder, limit, offset)
	if e != nil {
		return e
	}
	defer rows.Close()
	for rows.Next() {
		var c Coding
		var val, ev string
		if e = rows.Scan(&c.DocID, &c.Book, &c.Version, &c.Variable, &c.Unit, &c.Coder, &val, &ev, &c.Source, &c.Note, &c.Updated, &c.Stale); e != nil {
			return e
		}
		if e = json.Unmarshal([]byte(val), &c.Value); e != nil {
			return e
		}
		if e = json.Unmarshal([]byte(ev), &c.Evidence); e != nil {
			return e
		}
		if e = fn(c); e != nil {
			return e
		}
	}
	return rows.Err()
}

func (s *Store) wideValues(id, book, version, coder string) (map[string]any, error) {
	out := map[string]any{}
	if book == "" {
		return out, nil
	}
	rows, e := s.DB.Query(`SELECT variable,value FROM codings WHERE doc_id=? AND book=? AND version=? AND coder=? AND unit=0 AND stale=0`, id, book, version, coder)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	for rows.Next() {
		var key, value string
		if e = rows.Scan(&key, &value); e != nil {
			return nil, e
		}
		var v any
		if e = json.Unmarshal([]byte(value), &v); e != nil {
			return nil, e
		}
		if b, ok := v.(bool); ok {
			if b {
				v = 1
			} else {
				v = 0
			}
		}
		out[key] = v
	}
	return out, rows.Err()
}

type CodingReport struct {
	Documents int      `json:"documents"`
	Written   int      `json:"written"`
	Conflicts []string `json:"conflicts"`
}

func (s *Store) ApplyCodebook(job, name, version, coder string) (CodingReport, error) {
	report := CodingReport{Conflicts: []string{}}
	b, e := s.Codebook(name, version)
	if e != nil {
		return report, e
	}
	if job != "" {
		if _, e = s.Job(job); e != nil {
			return report, e
		}
	}
	compiled := map[string]*regexp.Regexp{}
	for _, v := range b.Variables {
		for _, rule := range v.Rules {
			compiled[rule.Pattern] = regexp.MustCompile(rule.Pattern)
		}
	}
	if coder == "" {
		coder = "rule:" + name + ":" + version
	}
	offset := 0
	for {
		ids, e := s.DocumentIDs(job, "", 500, offset)
		if e != nil {
			return report, e
		}
		if len(ids) == 0 {
			break
		}
		offset += len(ids)
		for _, id := range ids {
			d, e := s.Document(id)
			if e != nil {
				return report, e
			}
			if d.BodyStatus != "available" {
				continue
			}
			report.Documents++
			for _, v := range b.Variables {
				units := []int{0}
				if v.Unit == "paragraph" {
					units = nil
					for _, p := range d.Paragraphs {
						units = append(units, p.Number)
					}
				}
				for _, unit := range units {
					c := Coding{DocID: id, Book: name, Version: version, Variable: v.Name, Unit: unit, Coder: coder, Source: "rule", Evidence: []Evidence{}}
					values := map[string]any{}
					for _, rule := range v.Rules {
						rx := compiled[rule.Pattern]
						for _, p := range d.Paragraphs {
							if unit > 0 && unit != p.Number {
								continue
							}
							if len(rule.Sections) > 0 {
								ok := false
								for _, section := range rule.Sections {
									if section == p.Section {
										ok = true
									}
								}
								if !ok {
									continue
								}
							}
							if loc := rx.FindStringIndex(p.Text); loc != nil && loc[1] > loc[0] {
								values[jsonText(rule.Value)] = rule.Value
								c.Evidence = append(c.Evidence, Evidence{p.Number, p.Text[loc[0]:loc[1]]})
							}
						}
					}
					if len(values) > 1 {
						report.Conflicts = append(report.Conflicts, fmt.Sprintf("%s/%s/%d 多个规则取值冲突，未写入", id, v.Name, unit))
						continue
					}
					if len(values) == 0 {
						if v.Default == nil {
							continue
						}
						c.Value = v.Default
						c.Note = "无规则命中，使用 codebook 明示的 default"
					} else {
						for _, value := range values {
							c.Value = value
						}
					}
					if e = s.writeCoding(c); e != nil {
						return report, e
					}
					report.Written++
				}
			}
		}
	}
	return report, nil
}
