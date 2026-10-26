package lsp

import "strings"

// docOf returns the doc comment of the declaration beginning at a rune offset:
// the run of line comments on the lines just above it, with no blank line
// between, each with its comment marker and the one space after it removed.
func (t *text) docOf(offset int) []string {
	var doc []string
	for line := int(t.position(offset).Line) - 1; line >= 0; line-- {
		comment, ok := strings.CutPrefix(strings.TrimSpace(t.line(line)), "//")
		if !ok {
			break
		}
		doc = append(doc, strings.TrimPrefix(comment, " "))
	}
	for i, j := 0, len(doc)-1; i < j; i, j = i+1, j-1 {
		doc[i], doc[j] = doc[j], doc[i]
	}
	return doc
}

// markdown renders a doc comment the way Go's are read: a blank line separates
// paragraphs, and a run of indented lines is preformatted code, with the
// indentation they share removed. Everything else is prose, whose markdown
// punctuation is escaped so that it reads as written.
func markdown(doc []string) string {
	var b strings.Builder
	for i := 0; i < len(doc); {
		if !indented(doc[i]) {
			b.WriteString(markdownEscaper.Replace(doc[i]))
			b.WriteString("\n")
			i++
			continue
		}

		end := i
		for j := i; j < len(doc) && (indented(doc[j]) || strings.TrimSpace(doc[j]) == ""); j++ {
			if indented(doc[j]) {
				end = j
			}
		}
		block := doc[i : end+1]
		b.WriteString("```\n")
		for _, line := range block {
			b.WriteString(strings.TrimPrefix(line, commonIndent(block)))
			b.WriteString("\n")
		}
		b.WriteString("```\n")
		i = end + 1
	}
	return strings.TrimSpace(b.String())
}

// indented reports whether a doc comment line is a line of code.
func indented(line string) bool {
	return strings.TrimSpace(line) != "" && (line[0] == ' ' || line[0] == '\t')
}

// commonIndent returns the leading whitespace every nonblank line shares.
func commonIndent(lines []string) string {
	var indent string
	first := true
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		lead := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		if first {
			indent, first = lead, false
			continue
		}
		for !strings.HasPrefix(lead, indent) {
			indent = indent[:len(indent)-1]
		}
	}
	return indent
}

// markdownEscaper escapes the punctuation markdown would read as formatting.
var markdownEscaper = strings.NewReplacer(
	`\`, `\\`, "`", "\\`", `*`, `\*`, `_`, `\_`, `[`, `\[`, `]`, `\]`,
	`<`, `\<`, `>`, `\>`, `#`, `\#`, `|`, `\|`,
)
