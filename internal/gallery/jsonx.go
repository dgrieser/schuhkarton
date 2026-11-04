package gallery

import (
	"encoding/json"
	"errors"
	"strings"
)

// Models sometimes send almost-JSON: an object wrapped in prose or code
// fences, trailing commas, single quotes, comments, Python literals, or a
// quote inside a text value that is not escaped ("ein "Halt"-Schild").
// lenientUnmarshal tries the text as it is, then each balanced object in
// it, each also after repairJSON. It reports the strict error when
// nothing works. (After LenientUnmarshal in github.com/dgrieser/nickpit.)
func lenientUnmarshal(s string, v any) error {
	s = strings.TrimSpace(s)
	if s == "" {
		return errors.New("empty content")
	}
	strict := json.Unmarshal([]byte(s), v)
	if strict == nil {
		return nil
	}
	for _, c := range jsonCandidates(s) {
		if json.Unmarshal([]byte(c), v) == nil || json.Unmarshal(repairJSON([]byte(c)), v) == nil {
			return nil
		}
	}
	// no balanced candidate (cut off, or a quote in a value threw the
	// scan off): repair from the first brace to the last
	if i, j := strings.IndexByte(s, '{'), strings.LastIndexByte(s, '}'); i >= 0 && j > i {
		if json.Unmarshal(repairJSON([]byte(s[i:j+1])), v) == nil {
			return nil
		}
	}
	return strict
}

// jsonCandidates lists the balanced JSON objects in s, in order: also one
// wrapped in an array or in prose. Braces inside strings do not count. An
// object that is cut off ends the list: what follows is inside it.
func jsonCandidates(s string) []string {
	var out []string
	for pos := 0; pos < len(s); pos++ {
		if e := commentEnd(s, pos); e > pos {
			pos = e - 1 // an object in a comment is no answer
			continue
		}
		if s[pos] != '{' {
			continue
		}
		const open, close = '{', '}'
		depth, inStr, esc, closed := 0, false, false, false
		for i := pos; i < len(s) && !closed; i++ {
			c := s[i]
			switch {
			case esc:
				esc = false
			case inStr && c == '\\':
				esc = true
			case c == '"':
				inStr = !inStr
			case inStr:
			case commentEnd(s, i) > i:
				i = commentEnd(s, i) - 1
			case c == open:
				depth++
			case c == close:
				if depth--; depth == 0 {
					out = append(out, s[pos:i+1])
					pos, closed = i, true
				}
			}
		}
		if !closed {
			// cut off: what follows is inside it, not an answer of its own
			break
		}
	}
	return out
}

// repairJSON fixes what models get wrong most: trailing commas, single
// quoted strings, // and /* */ comments, True/False/None, and double quotes
// or apostrophes inside a string that are not escaped (a quote ends a
// string only when a comma, colon, closing bracket or the end follows it).
func repairJSON(data []byte) []byte {
	out := make([]byte, 0, len(data)+16)
	inStr, quote, esc, comma := false, byte(0), false, false
	for i := 0; i < len(data); i++ {
		c := data[i]
		// a comma waits for the next token: none before a closing bracket
		// (trailing comma), also when comments come between
		if comma && !inStr {
			switch {
			case c == ' ', c == '\t', c == '\n', c == '\r',
				c == '/' && i+1 < len(data) && (data[i+1] == '/' || data[i+1] == '*'):
			case c == '}', c == ']':
				comma = false
			default:
				out = append(out, ',')
				comma = false
			}
		}
		if esc {
			out = append(out, c)
			esc = false
			continue
		}
		if inStr {
			switch {
			case c == '\\':
				if quote == '\'' && i+1 < len(data) && data[i+1] == '\'' {
					out = append(out, '\'') // \' is no JSON escape
					i++
					continue
				}
				out = append(out, c)
				esc = true
			case c == quote && endsString(data, i+1):
				inStr = false
				out = append(out, '"')
			case c == '"':
				out = append(out, '\\', '"') // a quote in the text
			case c == '\n':
				out = append(out, '\\', 'n')
			case c == '\r', c == '\t':
				out = append(out, ' ')
			default:
				out = append(out, c)
			}
			continue
		}
		switch {
		case c == '/' && i+1 < len(data) && data[i+1] == '/':
			for i < len(data) && data[i] != '\n' {
				i++
			}
			out = append(out, '\n')
		case c == '/' && i+1 < len(data) && data[i+1] == '*':
			i += 2
			for i+1 < len(data) && (data[i] != '*' || data[i+1] != '/') {
				i++
			}
			i++
		case c == '"' || c == '\'':
			inStr, quote = true, c
			out = append(out, '"')
		case c == ',':
			comma = true
		case isIdentByte(c) && (i == 0 || !isIdentByte(data[i-1])):
			j := i
			for j < len(data) && isIdentByte(data[j]) {
				j++
			}
			switch w := string(data[i:j]); w {
			case "True", "False", "None":
				out = append(out, map[string]string{"True": "true", "False": "false", "None": "null"}[w]...)
			default:
				out = append(out, w...)
			}
			i = j - 1
		default:
			out = append(out, c)
		}
	}
	return out
}

// endsString: after a quote at data[i-1], does what follows close the
// string? Whitespace, then : } ] or the end; or a comma followed by what
// starts a JSON value, key or comment (a comma followed by a word is text: "a "b",
// c").
func endsString(data []byte, i int) bool {
	i = skipSpace(data, i)
	if i >= len(data) {
		return true
	}
	switch data[i] {
	case ':', '}', ']':
		return true
	case ',':
		j := skipSpace(data, i+1)
		if j >= len(data) {
			return true
		}
		switch c := data[j]; {
		case c == '"', c == '\'', c == '{', c == '[', c == '}', c == ']', c == '-', c == '/', c >= '0' && c <= '9':
			return true
		}
		for _, w := range []string{"true", "false", "null", "True", "False", "None"} {
			if strings.HasPrefix(string(data[j:]), w) {
				return true
			}
		}
	}
	return false
}

// commentEnd returns where a // or /* */ comment starting at s[i] ends, or
// i when none starts there. "//" right after a colon is no comment
// (http://).
func commentEnd(s string, i int) int {
	if i+1 >= len(s) || s[i] != '/' {
		return i
	}
	switch {
	case s[i+1] == '/' && (i == 0 || s[i-1] != ':'):
		if e := strings.IndexByte(s[i:], '\n'); e >= 0 {
			return i + e
		}
		return len(s)
	case s[i+1] == '*':
		if e := strings.Index(s[i+2:], "*/"); e >= 0 {
			return i + 2 + e + 2
		}
		return len(s)
	}
	return i
}

// skipSpace skips whitespace and // or /* */ comments from data[i].
func skipSpace(data []byte, i int) int {
	for i < len(data) {
		switch c := data[i]; {
		case c == ' ', c == '\t', c == '\n', c == '\r':
			i++
		case c == '/' && i+1 < len(data) && data[i+1] == '/':
			for i < len(data) && data[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(data) && data[i+1] == '*':
			end := strings.Index(string(data[i+2:]), "*/")
			if end < 0 {
				return len(data)
			}
			i += end + 4
		default:
			return i
		}
	}
	return i
}

func isIdentByte(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}
