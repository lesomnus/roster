package ldap

import (
	"errors"
	"strings"
)

// rdn is one relative distinguished name: `uid=kim`. Multi-valued RDNs
// (`cn=a+sn=b`) are not in this tree and are refused rather than half-read.
type rdn struct {
	attr  string // lower-cased
	value string // as written, unescaped
}

// dn is a distinguished name as its RDNs, leftmost first, RFC 4514.
type dn []rdn

// parseDN reads RFC 4514's string form, enough of it for the names this tree
// has: `type=value` parts separated by commas, with backslash escapes and
// hex pairs in values. Spaces around the separators are dropped.
func parseDN(s string) (dn, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return dn{}, nil
	}

	var out dn
	var cur strings.Builder
	var attr string
	inValue := false
	flush := func() error {
		if !inValue {
			return errors.New("an RDN with no '='")
		}
		v := strings.TrimRight(cur.String(), " ")
		out = append(out, rdn{attr: strings.ToLower(strings.TrimSpace(attr)), value: v})
		cur.Reset()
		attr, inValue = "", false

		return nil
	}

	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\\' && inValue:
			if i+1 >= len(s) {
				return nil, errors.New("a trailing backslash")
			}
			n := s[i+1]
			if isHex(n) && i+2 < len(s) && isHex(s[i+2]) {
				cur.WriteByte(unhex(n)<<4 | unhex(s[i+2]))
				i += 2
			} else {
				cur.WriteByte(n)
				i++
			}
		case c == '=' && !inValue:
			attr = cur.String()
			if strings.TrimSpace(attr) == "" {
				return nil, errors.New("an RDN with no type")
			}
			cur.Reset()
			inValue = true
			// Leading spaces of a value are not part of it.
			for i+1 < len(s) && s[i+1] == ' ' {
				i++
			}
		case c == ',' || c == ';':
			if err := flush(); err != nil {
				return nil, err
			}
		case c == '+' && inValue:
			return nil, errors.New("a multi-valued RDN")
		default:
			cur.WriteByte(c)
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}

	return out, nil
}

// String is the canonical form, escaped, with no spaces.
func (d dn) String() string {
	parts := make([]string, len(d))
	for i, r := range d {
		parts[i] = r.attr + "=" + escapeValue(r.value)
	}

	return strings.Join(parts, ",")
}

// hasSuffix is whether `suffix` is the tail of `d`, RDN by RDN, comparing
// types and values without regard to case -- which is what the matching
// rules of every attribute this tree uses as an RDN say.
func (d dn) hasSuffix(suffix dn) bool {
	if len(suffix) > len(d) {
		return false
	}
	off := len(d) - len(suffix)
	for i, r := range suffix {
		if d[off+i].attr != r.attr || !strings.EqualFold(d[off+i].value, r.value) {
			return false
		}
	}

	return true
}

// child is `r` under `d`.
func (d dn) child(attr, value string) dn {
	out := make(dn, 0, len(d)+1)
	out = append(out, rdn{attr: attr, value: value})

	return append(out, d...)
}

// escapeValue is RFC 4514 § 2.4.
func escapeValue(v string) string {
	if v == "" {
		return v
	}
	var b strings.Builder
	for i := 0; i < len(v); i++ {
		c := v[i]
		switch {
		case c == '"' || c == '+' || c == ',' || c == ';' || c == '<' || c == '>' || c == '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		case (c == ' ' || c == '#') && i == 0, c == ' ' && i == len(v)-1:
			b.WriteByte('\\')
			b.WriteByte(c)
		case c == 0:
			b.WriteString(`\00`)
		default:
			b.WriteByte(c)
		}
	}

	return b.String()
}

func isHex(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

func unhex(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	default:
		return c - 'A' + 10
	}
}
