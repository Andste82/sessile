package envfile

import (
	"bytes"
	"fmt"
	"strings"
)

// Parse reads a .env file. It is deliberately small: KEY=VALUE lines, an
// optional `export ` prefix, comments, single and double quotes, and nothing
// else — no variable expansion, no command substitution, no multi-line
// quoted values. A line that cannot be accepted is skipped and reported as
// ".env line N: <reason>"; reasons may name the variable but never show a
// value. Returned entries have Secret=false and Default=false, sorted by
// name; the last of duplicate names wins.
func Parse(data []byte) (entries []Entry, warnings []string) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))

	byName := map[string]int{} // name -> index in entries
	total := 0
	warn := func(n int, format string, a ...any) {
		warnings = append(warnings, fmt.Sprintf(".env line %d: %s", n, fmt.Sprintf(format, a...)))
	}

	for i, raw := range strings.Split(string(data), "\n") {
		n := i + 1
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if line == "" || line[0] == '#' {
			continue
		}
		if rest, ok := strings.CutPrefix(line, "export"); ok && rest != "" && (rest[0] == ' ' || rest[0] == '\t') {
			line = strings.TrimLeft(rest, " \t")
		}
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			warn(n, "not a KEY=VALUE line")
			continue
		}
		name := strings.TrimSpace(line[:eq])
		if !validName(name) || len(name) > MaxNameLen {
			warn(n, "invalid variable name")
			continue
		}
		if IsReserved(name) {
			warn(n, "%s is reserved, skipped", name)
			continue
		}
		value, reason := parseValue(line[eq+1:])
		if reason != "" {
			warn(n, "%s: %s", name, reason)
			continue
		}
		if strings.IndexByte(value, 0) >= 0 {
			warn(n, "%s: value contains NUL", name)
			continue
		}
		if len(value) > MaxValueLen {
			warn(n, "%s: value too long", name)
			continue
		}
		idx, dup := byName[name]
		newTotal := total + len(name) + len(value)
		if dup {
			newTotal -= len(name) + len(entries[idx].Value)
		} else if len(entries) >= MaxEntries {
			warn(n, "%s: too many entries, skipped", name)
			continue
		}
		if newTotal > MaxDotenvBytes {
			warn(n, "%s: .env too large, skipped", name)
			continue
		}
		total = newTotal
		if dup {
			entries[idx].Value = value
		} else {
			byName[name] = len(entries)
			entries = append(entries, Entry{Name: name, Value: value})
		}
	}
	sortEntries(entries)
	return entries, warnings
}

// parseValue interprets what follows the '='. reason is non-empty when the
// line must be skipped.
func parseValue(s string) (value, reason string) {
	t := strings.TrimLeft(s, " \t")
	if t == "" {
		return "", ""
	}
	switch t[0] {
	case '\'':
		end := strings.IndexByte(t[1:], '\'')
		if end < 0 {
			return "", "unterminated quote"
		}
		if !onlyComment(t[end+2:]) {
			return "", "unexpected text after closing quote"
		}
		return t[1 : 1+end], ""
	case '"':
		var b strings.Builder
		for i := 1; i < len(t); i++ {
			c := t[i]
			switch c {
			case '"':
				if !onlyComment(t[i+1:]) {
					return "", "unexpected text after closing quote"
				}
				return b.String(), ""
			case '\\':
				if i+1 < len(t) {
					switch t[i+1] {
					case 'n':
						b.WriteByte('\n')
					case 'r':
						b.WriteByte('\r')
					case 't':
						b.WriteByte('\t')
					case '\\', '"', '$':
						b.WriteByte(t[i+1])
					default:
						b.WriteByte('\\')
						b.WriteByte(t[i+1])
					}
					i++
					continue
				}
				b.WriteByte('\\')
			default:
				b.WriteByte(c)
			}
		}
		return "", "unterminated quote"
	}
	// Unquoted: ends at the first whitespace-introduced '#'. The leading
	// whitespace is part of the search so `KEY= #c` is an empty value.
	if i := indexInlineComment(s); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s), ""
}

func indexInlineComment(s string) int {
	for i := 0; i+1 < len(s); i++ {
		if (s[i] == ' ' || s[i] == '\t') && s[i+1] == '#' {
			return i
		}
	}
	return -1
}

func onlyComment(rest string) bool {
	rest = strings.TrimSpace(rest)
	return rest == "" || rest[0] == '#'
}
