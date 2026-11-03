package deb

import (
	"bufio"
	"bytes"
	"fmt"
	"strings"
)

// Field is one control-file field. Value keeps continuation lines verbatim
// (each starting with a space), joined by "\n".
type Field struct {
	Name  string
	Value string
}

// Paragraph is an ordered deb822 paragraph.
type Paragraph []Field

// Get returns a field value (case-insensitive name), or "".
func (p Paragraph) Get(name string) string {
	for _, f := range p {
		if strings.EqualFold(f.Name, name) {
			return strings.TrimSpace(f.Value)
		}
	}
	return ""
}

// Without returns a copy of p minus the named fields.
func (p Paragraph) Without(names ...string) Paragraph {
	out := make(Paragraph, 0, len(p))
outer:
	for _, f := range p {
		for _, n := range names {
			if strings.EqualFold(f.Name, n) {
				continue outer
			}
		}
		out = append(out, f)
	}
	return out
}

// Write serializes p (without the trailing blank separator line).
func (p Paragraph) Write(b *bytes.Buffer) {
	for _, f := range p {
		b.WriteString(f.Name)
		b.WriteString(":")
		if f.Value != "" && !strings.HasPrefix(f.Value, "\n") {
			b.WriteString(" ")
		}
		b.WriteString(f.Value)
		b.WriteString("\n")
	}
}

// ParseParagraph parses a single deb822 paragraph such as DEBIAN/control.
func ParseParagraph(data []byte) (Paragraph, error) {
	var p Paragraph
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	line := 0
	for sc.Scan() {
		line++
		t := strings.TrimRight(sc.Text(), "\r")
		switch {
		case t == "":
			if len(p) > 0 {
				// Trailing blank lines are fine; a second paragraph is not.
				for sc.Scan() {
					if strings.TrimSpace(sc.Text()) != "" {
						return nil, fmt.Errorf("control: unexpected second paragraph at line %d", line+1)
					}
				}
			}
		case strings.HasPrefix(t, "#"):
		case t[0] == ' ' || t[0] == '\t':
			if len(p) == 0 {
				return nil, fmt.Errorf("control: continuation before any field at line %d", line)
			}
			last := &p[len(p)-1]
			last.Value += "\n" + t
		default:
			name, val, ok := strings.Cut(t, ":")
			if !ok || name == "" || strings.ContainsAny(name, " \t") {
				return nil, fmt.Errorf("control: malformed line %d: %q", line, t)
			}
			p = append(p, Field{Name: name, Value: strings.TrimSpace(val)})
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(p) == 0 {
		return nil, fmt.Errorf("control: empty")
	}
	return p, nil
}
