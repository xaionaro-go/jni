// Package rconsts parses the aapt2 R.txt symbol table emitted by
// `aapt2 link --output-text-symbols` and emits a deterministic Go file of
// `R_<type>_<name>` constants so Go callers can reach Android resource IDs
// without going through R.java reflection.
package rconsts

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// Entry is one R.txt row. For the int[] styleable rows IsArray is true and
// ArrayHexValues holds the bracketed hex IDs in their original order; Hex is
// empty in that case. For the int rows Hex holds the single hex literal and
// ArrayHexValues is nil.
type Entry struct {
	Type           string
	Name           string
	Hex            string
	IsArray        bool
	ArrayHexValues []string
}

// AcceptedTypes is the set of R sub-types this tool understands. It is the
// union of the cycle-4 task list (layout, string, color, dimen, attr, style,
// drawable, id, menu, mipmap, xml, styleable) and the additional sub-types
// aapt2 emits in real Material/AppCompat builds (anim, animator, bool,
// integer, interpolator, plurals). Anything else is rejected so a typo in a
// hand-edited R.txt fails loudly instead of being silently dropped.
var AcceptedTypes = map[string]struct{}{
	"anim":         {},
	"animator":     {},
	"attr":         {},
	"bool":         {},
	"color":        {},
	"dimen":        {},
	"drawable":     {},
	"id":           {},
	"integer":      {},
	"interpolator": {},
	"layout":       {},
	"menu":         {},
	"mipmap":       {},
	"plurals":      {},
	"string":       {},
	"style":        {},
	"styleable":    {},
	"xml":          {},
}

// EmitOrder is the deterministic top-level group order. styleable is handled
// separately by Emit (arrays then per-attribute index constants), so it is
// not present here.
var EmitOrder = []string{
	"layout",
	"string",
	"color",
	"dimen",
	"attr",
	"style",
	"drawable",
	"id",
	"menu",
	"mipmap",
	"xml",
	"anim",
	"animator",
	"bool",
	"integer",
	"interpolator",
	"plurals",
}

// Parse consumes an R.txt stream and returns the entries in source order.
// Blank lines and lines starting with '#' are skipped. Every other line must
// match either:
//
//	int <type> <name> <hex>
//	int[] styleable <name> { <hex>, <hex>, ... }
//
// Any other shape, or an unknown sub-type, is reported with the offending
// line number so a malformed R.txt is caught at parse time rather than
// generating bogus Go.
func Parse(r io.Reader) ([]Entry, error) {
	var entries []Entry
	scanner := bufio.NewScanner(r)
	// R.txt styleable lines for large attr sets exceed the default 64 KiB
	// scanner buffer; raise the limit so we never silently truncate.
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		entry, err := parseLine(line)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNo, err)
		}
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read R.txt: %w", err)
	}
	return entries, nil
}

func parseLine(line string) (Entry, error) {
	switch {
	case strings.HasPrefix(line, "int[] "):
		return parseArrayLine(strings.TrimPrefix(line, "int[] "))
	case strings.HasPrefix(line, "int "):
		return parseScalarLine(strings.TrimPrefix(line, "int "))
	default:
		return Entry{}, fmt.Errorf("expected `int ` or `int[] ` prefix, got %q", line)
	}
}

func parseScalarLine(rest string) (Entry, error) {
	fields := strings.Fields(rest)
	if len(fields) != 3 {
		return Entry{}, fmt.Errorf("scalar line must be `<type> <name> <hex>`, got %d fields", len(fields))
	}
	typ, name, hex := fields[0], fields[1], fields[2]
	if _, ok := AcceptedTypes[typ]; !ok {
		return Entry{}, fmt.Errorf("unknown R sub-type %q", typ)
	}
	if !strings.HasPrefix(hex, "0x") && !isUnsignedDecimal(hex) {
		return Entry{}, fmt.Errorf("expected hex literal or decimal index, got %q", hex)
	}
	return Entry{Type: typ, Name: name, Hex: hex}, nil
}

func parseArrayLine(rest string) (Entry, error) {
	open := strings.IndexByte(rest, '{')
	close := strings.LastIndexByte(rest, '}')
	if open < 0 || close < 0 || close <= open {
		return Entry{}, fmt.Errorf("array line missing braces: %q", rest)
	}
	header := strings.Fields(rest[:open])
	if len(header) != 2 {
		return Entry{}, fmt.Errorf("array header must be `<type> <name>`, got %d fields", len(header))
	}
	typ, name := header[0], header[1]
	if typ != "styleable" {
		return Entry{}, fmt.Errorf("only `styleable` arrays are valid in R.txt, got %q", typ)
	}
	body := strings.TrimSpace(rest[open+1 : close])
	var values []string
	if body != "" {
		for _, raw := range strings.Split(body, ",") {
			v := strings.TrimSpace(raw)
			if v == "" {
				return Entry{}, fmt.Errorf("empty value in styleable array %q", name)
			}
			if !strings.HasPrefix(v, "0x") {
				return Entry{}, fmt.Errorf("expected hex literal in styleable array %q, got %q", name, v)
			}
			values = append(values, v)
		}
	}
	return Entry{Type: typ, Name: name, IsArray: true, ArrayHexValues: values}, nil
}

func isUnsignedDecimal(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
