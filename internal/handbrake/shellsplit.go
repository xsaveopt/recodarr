package handbrake

import "strings"

func splitShellArgs(s string) []string {
	var (
		out                []string
		cur                strings.Builder
		inSingle, inDouble bool
		hasToken           bool
	)
	flush := func() {
		if hasToken {
			out = append(out, cur.String())
			cur.Reset()
			hasToken = false
		}
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inSingle:
			if c == '\'' {
				inSingle = false
			} else {
				cur.WriteByte(c)
				hasToken = true
			}
		case inDouble:
			switch c {
			case '"':
				inDouble = false
			case '\\':
				if i+1 < len(s) {
					i++
					cur.WriteByte(s[i])
					hasToken = true
				}
			default:
				cur.WriteByte(c)
				hasToken = true
			}
		default:
			switch c {
			case ' ', '\t', '\n', '\r':
				flush()
			case '\'':
				inSingle = true
				hasToken = true
			case '"':
				inDouble = true
				hasToken = true
			case '\\':
				if i+1 < len(s) {
					i++
					cur.WriteByte(s[i])
					hasToken = true
				}
			default:
				cur.WriteByte(c)
				hasToken = true
			}
		}
	}
	flush()
	return out
}
