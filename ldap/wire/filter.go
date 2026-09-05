package wire

import (
	"errors"
	"fmt"
	"strings"

	ber "github.com/go-asn1-ber/asn1-ber"
)

// FilterOp is the kind of a filter node, RFC 4511 § 4.5.1.7.
type FilterOp int

const (
	// FilterAnd, FilterOr and FilterNot have Children.
	FilterAnd FilterOp = iota
	FilterOr
	FilterNot
	// FilterEqual has Attr and Value.
	FilterEqual
	// FilterSubstrings has Attr and Initial, Any, Final; at least one of them.
	FilterSubstrings
	// FilterPresent has Attr.
	FilterPresent
	// FilterUndefined is a filter this package does not evaluate -- ordering,
	// approximate and extensible matches -- and RFC 4511 § 4.5.1.7 says what
	// an undefined filter does: it matches nothing, and a `not` of it matches
	// nothing too. Kept as a node so `(|(cn=kim)(cn~=kim))` still finds kim.
	FilterUndefined
)

// Filter is the search filter as it arrived: a tree, never a string.
type Filter struct {
	Op       FilterOp
	Attr     string
	Value    string
	Initial  string
	Any      []string
	Final    string
	Children []*Filter
}

// String is the RFC 4515 form, for logs and for a test to read.
func (f *Filter) String() string {
	if f == nil {
		return ""
	}
	var b strings.Builder
	b.WriteByte('(')
	switch f.Op {
	case FilterAnd, FilterOr, FilterNot:
		b.WriteByte(map[FilterOp]byte{FilterAnd: '&', FilterOr: '|', FilterNot: '!'}[f.Op])
		for _, c := range f.Children {
			b.WriteString(c.String())
		}
	case FilterEqual:
		fmt.Fprintf(&b, "%s=%s", f.Attr, f.Value)
	case FilterPresent:
		fmt.Fprintf(&b, "%s=*", f.Attr)
	case FilterSubstrings:
		fmt.Fprintf(&b, "%s=%s*", f.Attr, f.Initial)
		for _, a := range f.Any {
			b.WriteString(a + "*")
		}
		b.WriteString(f.Final)
	case FilterUndefined:
		fmt.Fprintf(&b, "%s?", f.Attr)
	}
	b.WriteByte(')')

	return b.String()
}

// Mentions is whether any node of the filter names the attribute, compared
// without regard to case as attribute descriptions are.
func (f *Filter) Mentions(attr string) bool {
	if f == nil {
		return false
	}
	if strings.EqualFold(f.Attr, attr) {
		return true
	}
	for _, c := range f.Children {
		if c.Mentions(attr) {
			return true
		}
	}

	return false
}

func decodeFilter(p *ber.Packet) (*Filter, error) {
	if p.ClassType != ber.ClassContext {
		return nil, errors.New("filter: not a context-tagged choice")
	}
	switch p.Tag {
	case 0, 1:
		f := &Filter{Op: FilterAnd}
		if p.Tag == 1 {
			f.Op = FilterOr
		}
		for _, c := range p.Children {
			child, err := decodeFilter(c)
			if err != nil {
				return nil, err
			}
			f.Children = append(f.Children, child)
		}

		return f, nil
	case 2:
		if len(p.Children) != 1 {
			return nil, errors.New("filter: a not of several")
		}
		child, err := decodeFilter(p.Children[0])
		if err != nil {
			return nil, err
		}

		return &Filter{Op: FilterNot, Children: []*Filter{child}}, nil
	case 3:
		if len(p.Children) != 2 {
			return nil, errors.New("filter: an equality without an attribute and a value")
		}

		return &Filter{Op: FilterEqual, Attr: string(p.Children[0].Data.Bytes()), Value: string(p.Children[1].Data.Bytes())}, nil
	case 4:
		if len(p.Children) != 2 {
			return nil, errors.New("filter: substrings without an attribute and parts")
		}
		f := &Filter{Op: FilterSubstrings, Attr: string(p.Children[0].Data.Bytes())}
		for _, part := range p.Children[1].Children {
			v := string(part.Data.Bytes())
			switch part.Tag {
			case 0:
				f.Initial = v
			case 1:
				f.Any = append(f.Any, v)
			case 2:
				f.Final = v
			default:
				return nil, errors.New("filter: a substring part that is not initial, any or final")
			}
		}
		if f.Initial == "" && len(f.Any) == 0 && f.Final == "" {
			return nil, errors.New("filter: substrings with no parts")
		}

		return f, nil
	case 7:
		return &Filter{Op: FilterPresent, Attr: string(p.Data.Bytes())}, nil
	case 5, 6, 8, 9:
		f := &Filter{Op: FilterUndefined}
		if len(p.Children) > 0 {
			f.Attr = string(p.Children[0].Data.Bytes())
		}

		return f, nil
	default:
		return nil, fmt.Errorf("filter: unknown choice %d", p.Tag)
	}
}
