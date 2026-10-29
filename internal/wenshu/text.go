package wenshu

import (
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/html"
)

func plainLines(s string) []string {
	z := html.NewTokenizer(strings.NewReader(s))
	var b strings.Builder
	skip := 0
	for {
		t := z.Next()
		if t == html.ErrorToken {
			if z.Err() == io.EOF {
				break
			}
			break
		}
		tok := z.Token()
		switch t {
		case html.StartTagToken:
			if tok.Data == "script" || tok.Data == "style" {
				skip++
			}
			if skip == 0 && blockTag(tok.Data) {
				b.WriteByte('\n')
			}
		case html.EndTagToken:
			if tok.Data == "script" || tok.Data == "style" {
				if skip > 0 {
					skip--
				}
			}
			if skip == 0 && blockTag(tok.Data) {
				b.WriteByte('\n')
			}
		case html.SelfClosingTagToken:
			if skip == 0 && blockTag(tok.Data) {
				b.WriteByte('\n')
			}
		case html.TextToken:
			if skip == 0 {
				b.WriteString(tok.Data)
			}
		}
	}
	lines := []string{}
	for _, line := range strings.Split(strings.ReplaceAll(b.String(), "\r", "\n"), "\n") {
		v := strings.Join(strings.FieldsFunc(line, unicode.IsSpace), " ")
		if v != "" {
			lines = append(lines, v)
		}
	}
	return lines
}
func blockTag(t string) bool {
	switch t {
	case "p", "div", "br", "li", "tr", "h1", "h2", "h3", "h4", "section", "article", "center":
		return true
	}
	return false
}

var sections = []struct{ Key, Name string }{{"s22", "首部"}, {"s53", "当事人段"}, {"s23", "诉讼记录"}, {"s24", "诉控辩"}, {"s25", "事实"}, {"s26", "理由"}, {"s27", "判决结果"}, {"s28", "尾部"}, {"s54", "其他"}}

func ExtractParagraphs(d Record) (string, []Paragraph) {
	content := field(d, "qwContent")
	if content == "" {
		for _, s := range sections {
			if v := field(d, s.Key); v != "" {
				content += v + "\n"
			}
		}
	}
	lines := plainLines(content)
	ps := make([]Paragraph, 0, len(lines))
	normalized := map[string]string{}
	for _, s := range sections {
		normalized[s.Name] = strings.Join(plainLines(field(d, s.Key)), "\n")
	}
	offset := 0
	for i, line := range lines {
		section := "全文"
		for _, s := range sections {
			if normalized[s.Name] != "" && strings.Contains(normalized[s.Name], line) {
				section = s.Name
				break
			}
		}
		end := offset + utf8.RuneCountInString(line)
		ps = append(ps, Paragraph{i + 1, section, line, offset, end})
		offset = end + 1
	}
	return strings.Join(lines, "\n"), ps
}
