package generator

import (
	"fmt"
	"strings"
)

// doc reproduces the legacy js-yaml output byte-for-byte for the fixed
// document shapes we emit: two-space indent, null/empty values rendered as
// "key: " (trailing space), booleans and ints plain.
type doc struct{ b strings.Builder }

func (d *doc) header(comment string) {
	d.b.WriteString("---\n# " + comment + "\n")
}

func indentOf(n int) string { return strings.Repeat(" ", n) }

// kv writes "key: value". nil or empty-string values render as "key: "
// (legacy '!!null': 'empty' style).
func (d *doc) kv(indent int, key string, val any) {
	d.b.WriteString(indentOf(indent))
	d.b.WriteString(key + ":")
	switch v := val.(type) {
	case nil:
		d.b.WriteString(" ")
	case string:
		if v == "" {
			d.b.WriteString(" ")
		} else {
			d.b.WriteString(" " + v)
		}
	case bool:
		if v {
			d.b.WriteString(" true")
		} else {
			d.b.WriteString(" false")
		}
	case int:
		d.b.WriteString(fmt.Sprintf(" %d", v))
	default:
		d.b.WriteString(fmt.Sprintf(" %v", v))
	}
	d.b.WriteString("\n")
}

// raw writes a preformatted line (used for "key:" map headers and "- " list
// item leads).
func (d *doc) raw(indent int, line string) {
	d.b.WriteString(indentOf(indent) + line + "\n")
}

func (d *doc) String() string { return d.b.String() }
