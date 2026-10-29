package wenshu

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

type LinkOptions struct {
	Text          string `json:"text"`
	LookupMissing bool   `json:"lookup_missing,omitempty"`
	JobID         string `json:"job_id,omitempty"`
	MaxLookups    int    `json:"max_lookups,omitempty"`
}
type ReferenceCandidate struct {
	DocID      string `json:"doc_id"`
	Title      string `json:"title"`
	CaseNumber string `json:"case_number"`
	Court      string `json:"court"`
	URL        string `json:"url"`
}
type LinkedReference struct {
	Reference  string               `json:"reference"`
	Kind       string               `json:"kind"`
	Status     string               `json:"status"`
	DocIDs     []string             `json:"doc_ids"`
	URL        string               `json:"url,omitempty"`
	Candidates []ReferenceCandidate `json:"candidates,omitempty"`
	Message    string               `json:"message,omitempty"`
}
type LinkResult struct {
	Text       string            `json:"text"`
	References []LinkedReference `json:"references"`
	Lookups    int               `json:"lookups"`
}
type referenceSpan struct {
	start, end  int
	value, kind string
}

var caseReference = regexp.MustCompile(`(?:\([0-9０-９]{4}\)|（[0-9０-９]{4}）|\[[0-9０-９]{4}\]|［[0-9０-９]{4}］|【[0-9０-９]{4}】)[\p{Han}A-Za-z0-9０-９ \t　]{1,40}?[0-9０-９]+[ \t　]*号(?:之[0-9０-９一二三四五六七八九十]+)?`)
var refDefinition = regexp.MustCompile(`(?m)^ {0,3}\[([^]\n]+)\]:[^\n]*(?:\n|$)`)

func normalizedReference(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		if r >= 0xff01 && r <= 0xff5e {
			r -= 0xfee0
		}
		switch r {
		case '[', '【':
			return '('
		case ']', '】':
			return ')'
		}
		return r
	}, strings.TrimSpace(s))
}
func normSQL(expr string) string {
	q := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	for _, r := range []rune{' ', '\t', '\r', '\n', '\v', '\f', '\u0085', '\u00a0', '\u1680', '\u2000', '\u2001', '\u2002', '\u2003', '\u2004', '\u2005', '\u2006', '\u2007', '\u2008', '\u2009', '\u200a', '\u2028', '\u2029', '\u202f', '\u205f', '\u3000'} {
		expr = "replace(" + expr + "," + q(string(r)) + ",'')"
	}
	for r := rune(0xff01); r <= 0xff5e; r++ {
		expr = "replace(" + expr + "," + q(string(r)) + "," + q(string(r-0xfee0)) + ")"
	}
	for _, p := range [][2]string{{"[", "("}, {"【", "("}, {"]", ")"}, {"】", ")"}} {
		expr = "replace(" + expr + "," + q(p[0]) + "," + q(p[1]) + ")"
	}
	return expr
}

const titleSQL = `coalesce(nullif(json_extract(detail,'$.s1'),''),json_extract(metadata,'$.1'),'')`
const caseSQL = `coalesce(nullif(json_extract(detail,'$.s7'),''),json_extract(metadata,'$.7'),'')`

func (s *Store) findReference(ctx context.Context, value, kind, job string) ([]ReferenceCandidate, error) {
	expr, index := titleSQL, "title"
	if kind == "case_number" {
		expr, index = caseSQL, "case"
	}
	expr = normSQL(expr)
	if _, e := s.DB.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS document_reference_`+index+` ON documents(`+expr+`)`); e != nil {
		return nil, e
	}
	rows, e := s.DB.QueryContext(ctx, `SELECT d.id,`+titleSQL+`,`+caseSQL+`,coalesce(nullif(json_extract(detail,'$.s2'),''),json_extract(metadata,'$.2'),''),coalesce((SELECT alias FROM doc_aliases a WHERE a.doc_id=d.id ORDER BY seen_at DESC,rowid DESC LIMIT 1),d.id) FROM documents d WHERE `+expr+`=? AND (?='' OR EXISTS(SELECT 1 FROM job_docs j WHERE j.job_id=? AND j.doc_id=d.id)) ORDER BY d.id LIMIT 11`, normalizedReference(value), job, job)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []ReferenceCandidate{}
	for rows.Next() {
		var c ReferenceCandidate
		var remote string
		if e = rows.Scan(&c.DocID, &c.Title, &c.CaseNumber, &c.Court, &remote); e != nil {
			return nil, e
		}
		c.URL = sourceURL(Document{ID: c.DocID, RemoteID: remote})
		out = append(out, c)
	}
	return out, rows.Err()
}
func candidateFromDocument(d Document) ReferenceCandidate {
	return ReferenceCandidate{d.ID, d.Title, d.CaseNumber, d.Court, sourceURL(d)}
}
func resolvedReference(raw, kind string, candidates []ReferenceCandidate) LinkedReference {
	r := LinkedReference{Reference: raw, Kind: kind, Status: "unresolved", DocIDs: []string{}}
	for _, c := range candidates {
		r.DocIDs = append(r.DocIDs, c.DocID)
	}
	switch {
	case len(candidates) > 1:
		r.Status = "ambiguous"
		r.Candidates = candidates
	case len(candidates) == 1:
		r.URL = candidates[0].URL
		r.Status = "linked"
		if r.URL == "" {
			r.Status = "matched_without_link"
		}
	}
	return r
}

func (a *App) LinkReferences(ctx context.Context, opt LinkOptions) (LinkResult, error) {
	result := LinkResult{Text: opt.Text, References: []LinkedReference{}}
	if len(opt.Text) > 512*1024 {
		return result, fmt.Errorf("text 最大为 512 KB")
	}
	if opt.MaxLookups == 0 {
		opt.MaxLookups = 10
	}
	if opt.MaxLookups < 1 || opt.MaxLookups > 100 {
		return result, fmt.Errorf("max_lookups 必须为 1..100")
	}
	if opt.JobID != "" {
		if _, e := a.Store.Job(opt.JobID); e != nil {
			return result, e
		}
	}
	spans := extractReferences(opt.Text)
	if len(spans) > 100 {
		return result, fmt.Errorf("一次最多处理 100 处文书引用")
	}
	cache := map[string]LinkedReference{}
	var b strings.Builder
	cursor := 0
	for _, span := range spans {
		key := span.kind + ":" + normalizedReference(span.value)
		r, ok := cache[key]
		if !ok {
			candidates, e := a.Store.findReference(ctx, span.value, span.kind, opt.JobID)
			if e != nil {
				return result, e
			}
			r = resolvedReference(span.value, span.kind, candidates)
			if len(candidates) == 0 && opt.LookupMissing {
				if result.Lookups >= opt.MaxLookups {
					r.Status = "lookup_limit"
					r.Message = "本次联网查找次数已用完"
				} else {
					result.Lookups++
					r = a.lookupReference(ctx, span.value, span.kind)
				}
			}
			cache[key] = r
		}
		raw := opt.Text[span.start:span.end]
		r.Reference = raw
		replacement := raw
		if r.Status == "linked" {
			label := strings.NewReplacer("\\", "\\\\", "[", "\\[", "]", "\\]", "*", "\\*", "_", "\\_", "`", "\\`", "<", "&lt;", ">", "&gt;").Replace(raw)
			replacement = "[" + label + "](" + r.URL + ")"
		}
		b.WriteString(opt.Text[cursor:span.start])
		b.WriteString(replacement)
		cursor = span.end
		result.References = append(result.References, r)
	}
	b.WriteString(opt.Text[cursor:])
	result.Text = b.String()
	return result, nil
}
func (a *App) lookupReference(ctx context.Context, value, kind string) LinkedReference {
	r := LinkedReference{Reference: value, Kind: kind, Status: "unresolved", DocIDs: []string{}}
	key := "s1"
	queryValue := value
	if kind == "case_number" {
		key = "s7"
		queryValue = strings.NewReplacer("(", "（", ")", "）").Replace(normalizedReference(value))
	}
	q := Query{Conditions: []Condition{{Key: key, Value: queryValue}}}
	records := []Record{}
	total := 0
	for page := 1; page <= 2; page++ {
		s, e := a.Client.Search(ctx, q, page, 5)
		if e != nil {
			r.Status = "lookup_error"
			r.Message = e.Error()
			return r
		}
		total = s.Total
		records = append(records, s.Records...)
		if page*5 >= total || total > 10 {
			break
		}
	}
	candidates := []ReferenceCandidate{}
	matching := []Record{}
	seen := map[string]bool{}
	for _, m := range records {
		test := field(m, "1", "s1")
		if kind == "case_number" {
			test = field(m, "7", "s7")
		}
		if normalizedReference(strings.Join(plainLines(test), " ")) != normalizedReference(value) {
			continue
		}
		id := field(m, "rowkey", "s5", "docId")
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		matching = append(matching, m)
		candidates = append(candidates, ReferenceCandidate{DocID: id, Title: field(m, "1", "s1"), CaseNumber: field(m, "7", "s7"), Court: field(m, "2", "s2"), URL: sourceURL(Document{ID: id})})
	}
	r = resolvedReference(value, kind, candidates)
	if total > len(records) {
		r.Status = "lookup_incomplete"
		r.URL = ""
		r.Candidates = candidates
		r.Message = "检索候选较多，可使用完整文书标题或案号进一步定位"
		return r
	}
	if len(matching) == 1 {
		id := candidates[0].DocID
		detail, e := a.Client.Detail(ctx, id)
		if e != nil {
			r.Status = "lookup_error"
			r.URL = ""
			r.Message = e.Error()
			return r
		}
		check := field(detail, "s1")
		if kind == "case_number" {
			check = field(detail, "s7")
		}
		if normalizedReference(strings.Join(plainLines(check), " ")) != normalizedReference(value) {
			r.Status = "unresolved"
			r.URL = ""
			r.Message = "正文中的文书信息与引用不一致"
			return r
		}
		if e = a.Store.SaveDetail(id, detail); e != nil {
			r.Status = "lookup_error"
			r.URL = ""
			r.Message = e.Error()
			return r
		}
		d, e := a.Store.Document(id)
		if e != nil {
			r.Status = "lookup_error"
			r.URL = ""
			r.Message = e.Error()
			return r
		}
		check = d.Title
		if kind == "case_number" {
			check = d.CaseNumber
		}
		if normalizedReference(check) != normalizedReference(value) {
			r.Status = "unresolved"
			r.URL = ""
			r.Message = "正文中的文书信息与引用不一致"
			return r
		}
		r = resolvedReference(value, kind, []ReferenceCandidate{candidateFromDocument(d)})
	}
	return r
}

func extractReferences(text string) []referenceSpan {
	protected := markdownProtected(text)
	spans := []referenceSpan{}
	clear := func(a, b int) bool {
		for i := a; i < b; i++ {
			if protected[i] {
				return false
			}
		}
		return true
	}
	for i := 0; i < len(text); {
		r, n := utf8.DecodeRuneInString(text[i:])
		if r != '《' || protected[i] {
			i += n
			continue
		}
		start := i
		inside := i + n
		depth := 1
		i += n
		for i < len(text) {
			c, w := utf8.DecodeRuneInString(text[i:])
			if c == '《' {
				depth++
			}
			if c == '》' {
				depth--
				if depth == 0 {
					end := i + w
					if clear(start, end) {
						value := strings.TrimSpace(text[inside:i])
						if value != "" {
							kind := "title"
							if loc := caseReference.FindStringIndex(value); loc != nil && loc[0] == 0 && loc[1] == len(value) {
								kind = "case_number"
							}
							spans = append(spans, referenceSpan{start, end, value, kind})
							for k := start; k < end; k++ {
								protected[k] = true
							}
						}
					}
					i = end
					break
				}
			}
			i += w
		}
	}
	for _, loc := range caseReference.FindAllStringIndex(text, -1) {
		if clear(loc[0], loc[1]) {
			spans = append(spans, referenceSpan{loc[0], loc[1], text[loc[0]:loc[1]], "case_number"})
		}
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })
	return spans
}
func markdownProtected(text string) []bool {
	marked := make([]bool, len(text))
	mark := func(a, b int) {
		for i := a; i < b && i < len(marked); i++ {
			marked[i] = true
		}
	}
	definitions := map[string]bool{}
	for _, loc := range refDefinition.FindAllStringSubmatchIndex(text, -1) {
		mark(loc[0], loc[1])
		definitions[strings.ToLower(strings.TrimSpace(text[loc[2]:loc[3]]))] = true
	}
	var fence byte
	var fenceLen int
	for start := 0; start < len(text); {
		end := strings.IndexByte(text[start:], '\n')
		if end < 0 {
			end = len(text)
		} else {
			end += start + 1
		}
		line := text[start:end]
		trim := strings.TrimLeft(line, " ")
		indent := len(line) - len(trim)
		for indent <= 3 && strings.HasPrefix(trim, ">") {
			trim = strings.TrimPrefix(trim, ">")
			trim = strings.TrimPrefix(trim, " ")
			remaining := strings.TrimLeft(trim, " ")
			indent = len(trim) - len(remaining)
			trim = remaining
		}
		run := 0
		if len(trim) > 0 && (trim[0] == '`' || trim[0] == '~') {
			for run < len(trim) && trim[run] == trim[0] {
				run++
			}
		}
		if fence != 0 {
			mark(start, end)
			if indent <= 3 && len(trim) > 0 && trim[0] == fence && run >= fenceLen && strings.TrimSpace(trim[run:]) == "" {
				fence = 0
			}
		} else if indent <= 3 && run >= 3 {
			fence = trim[0]
			fenceLen = run
			mark(start, end)
		} else if indent >= 4 || strings.HasPrefix(line, "\t") {
			mark(start, end)
		}
		start = end
	}
	lower := strings.ToLower(text)
	for i := 0; i < len(text); {
		if marked[i] {
			i++
			continue
		}
		if text[i] == '\\' {
			_, n := utf8.DecodeRuneInString(text[min(i+1, len(text)):])
			mark(i, min(len(text), i+1+n))
			i += 1 + n
			continue
		}
		if text[i] == '`' {
			n := 1
			for i+n < len(text) && text[i+n] == '`' {
				n++
			}
			end := i + n
			found := -1
			for end < len(text) {
				p := strings.IndexByte(text[end:], '`')
				if p < 0 {
					break
				}
				p += end
				k := 1
				for p+k < len(text) && text[p+k] == '`' {
					k++
				}
				if k == n {
					found = p + k
					break
				}
				end = p + k
			}
			if found > 0 {
				mark(i, found)
				i = found
				continue
			}
			i += n
			continue
		}
		if text[i] == '[' {
			close := balancedEnd(text, i, '[', ']')
			if close > 0 {
				next := close + 1
				end := next
				if next < len(text) && text[next] == '(' {
					if c := balancedEnd(text, next, '(', ')'); c > 0 {
						end = c + 1
					}
				} else if next < len(text) && text[next] == '[' {
					if c := balancedEnd(text, next, '[', ']'); c > 0 {
						end = c + 1
					}
				} else if definitions[strings.ToLower(strings.TrimSpace(text[i+1:close]))] {
					end = close + 1
				} else {
					end = i
				}
				if end > i {
					mark(i, end)
					i = end
					continue
				}
			}
		}
		if text[i] == '<' {
			if strings.HasPrefix(text[i:], "<!--") {
				if end := strings.Index(text[i+4:], "-->"); end >= 0 {
					end += i + 7
					mark(i, end)
					i = end
					continue
				}
			}
			handled := false
			for _, tag := range []string{"code", "pre", "script", "style"} {
				if strings.HasPrefix(lower[i:], "<"+tag+">") || strings.HasPrefix(lower[i:], "<"+tag+" ") {
					if end := strings.Index(lower[i:], "</"+tag+">"); end >= 0 {
						end += i + len(tag) + 3
						mark(i, end)
						i = end
						handled = true
						break
					}
				}
			}
			if handled {
				continue
			}
			if end := strings.IndexByte(text[i:], '>'); end >= 0 {
				end += i + 1
				mark(i, end)
				i = end
				continue
			}
		}
		_, n := utf8.DecodeRuneInString(text[i:])
		i += n
	}
	return marked
}
func balancedEnd(text string, start int, open, close byte) int {
	depth := 0
	for i := start; i < len(text); i++ {
		if text[i] == '\\' {
			i++
			continue
		}
		if text[i] == open {
			depth++
		}
		if text[i] == close {
			depth--
			if depth == 0 {
				return i
			}
		}
		if text[i] == '\n' && open == '[' {
			return -1
		}
	}
	return -1
}
