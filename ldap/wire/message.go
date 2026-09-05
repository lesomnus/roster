package wire

import (
	"errors"
	"fmt"
	"time"

	ber "github.com/go-asn1-ber/asn1-ber"
)

// The protocol operations, RFC 4511 § 4.1.1: application-class tags on the
// message's second element. A response tag is its request's plus one for
// every operation this package refuses, which is what the refusal table in
// the loop relies on.
const (
	tagBindRequest      ber.Tag = 0
	tagBindResponse     ber.Tag = 1
	tagUnbindRequest    ber.Tag = 2
	tagSearchRequest    ber.Tag = 3
	tagSearchResultEnt  ber.Tag = 4
	tagSearchResultDone ber.Tag = 5
	tagModifyRequest    ber.Tag = 6
	tagAddRequest       ber.Tag = 8
	tagDelRequest       ber.Tag = 10
	tagModifyDNRequest  ber.Tag = 12
	tagCompareRequest   ber.Tag = 14
	tagAbandonRequest   ber.Tag = 16
	tagExtendedRequest  ber.Tag = 23
	tagExtendedResponse ber.Tag = 24
)

// message is one LDAPMessage: an identifier, an operation, and controls.
type message struct {
	id       int64
	op       *ber.Packet
	controls []control
}

type control struct {
	oid      string
	critical bool
	value    []byte
}

// criticalUnknown is the code to refuse an operation with when it carries a
// critical control this package does not read, and zero when it does not.
func (m *message) criticalUnknown() int {
	for _, c := range m.controls {
		if c.critical && c.oid != OidPagedResults {
			return UnavailableCriticalExtension
		}
	}

	return 0
}

func (m *message) paging() (*Paging, error) {
	for _, c := range m.controls {
		if c.oid != OidPagedResults {
			continue
		}
		v, err := ber.DecodePacketErr(c.value)
		if err != nil || len(v.Children) != 2 {
			return nil, errors.New("paged results control: not a size and a cookie")
		}
		size, err := ber.ParseInt64(v.Children[0].Data.Bytes())
		if err != nil {
			return nil, fmt.Errorf("paged results control: %w", err)
		}

		return &Paging{Size: int(size), Cookie: v.Children[1].Data.Bytes()}, nil
	}

	return nil, nil
}

func decodeMessage(p *ber.Packet) (*message, error) {
	if p.ClassType != ber.ClassUniversal || p.Tag != ber.TagSequence || len(p.Children) < 2 {
		return nil, errors.New("not an LDAPMessage")
	}
	id, err := ber.ParseInt64(p.Children[0].Data.Bytes())
	if err != nil {
		return nil, fmt.Errorf("message id: %w", err)
	}
	op := p.Children[1]
	if op.ClassType != ber.ClassApplication {
		return nil, errors.New("not a protocol operation")
	}
	m := &message{id: id, op: op}

	if len(p.Children) > 2 {
		cs := p.Children[2]
		if cs.ClassType != ber.ClassContext || cs.Tag != 0 {
			return nil, errors.New("not a controls sequence")
		}
		for _, c := range cs.Children {
			if len(c.Children) == 0 {
				return nil, errors.New("an empty control")
			}
			v := control{oid: string(c.Children[0].Data.Bytes())}
			for _, rest := range c.Children[1:] {
				switch rest.Tag {
				case ber.TagBoolean:
					b := rest.Data.Bytes()
					v.critical = len(b) > 0 && b[0] != 0
				case ber.TagOctetString:
					v.value = rest.Data.Bytes()
				}
			}
			m.controls = append(m.controls, v)
		}
	}

	return m, nil
}

// decodeBind reads a BindRequest. `sasl` reports the authentication choice
// this package does not speak.
func decodeBind(m *message) (req BindRequest, sasl bool, err error) {
	op := m.op
	if len(op.Children) != 3 {
		return req, false, errors.New("bind: not a version, a name and an authentication")
	}
	version, err := ber.ParseInt64(op.Children[0].Data.Bytes())
	if err != nil || version != ProtocolVersion {
		return req, false, fmt.Errorf("bind: LDAPv%d is not served; this is LDAPv3", version)
	}
	req.DN = string(op.Children[1].Data.Bytes())
	auth := op.Children[2]
	switch {
	case auth.ClassType == ber.ClassContext && auth.Tag == 0:
		req.Password = auth.Data.Bytes()
	case auth.ClassType == ber.ClassContext && auth.Tag == 3:
		return req, true, nil
	default:
		return req, false, errors.New("bind: an authentication choice that is neither simple nor SASL")
	}

	return req, false, nil
}

func decodeSearch(m *message) (SearchRequest, error) {
	op := m.op
	if len(op.Children) != 8 {
		return SearchRequest{}, errors.New("search: not the eight fields of a SearchRequest")
	}
	scope, err := ber.ParseInt64(op.Children[1].Data.Bytes())
	if err != nil || scope < 0 || scope > 2 {
		return SearchRequest{}, errors.New("search: scope")
	}
	size, err := ber.ParseInt64(op.Children[3].Data.Bytes())
	if err != nil || size < 0 {
		return SearchRequest{}, errors.New("search: size limit")
	}
	limit, err := ber.ParseInt64(op.Children[4].Data.Bytes())
	if err != nil || limit < 0 {
		return SearchRequest{}, errors.New("search: time limit")
	}
	only := op.Children[5].Data.Bytes()
	f, err := decodeFilter(op.Children[6])
	if err != nil {
		return SearchRequest{}, fmt.Errorf("search: %w", err)
	}
	req := SearchRequest{
		BaseDN:    string(op.Children[0].Data.Bytes()),
		Scope:     Scope(scope),
		SizeLimit: int(size),
		TimeLimit: time.Duration(limit) * time.Second,
		TypesOnly: len(only) > 0 && only[0] != 0,
		Filter:    f,
	}
	for _, a := range op.Children[7].Children {
		req.Attributes = append(req.Attributes, string(a.Data.Bytes()))
	}
	req.Paging, err = m.paging()
	if err != nil {
		return SearchRequest{}, err
	}

	return req, nil
}

func decodeExtended(m *message) (name string, value []byte, err error) {
	for _, c := range m.op.Children {
		if c.ClassType != ber.ClassContext {
			continue
		}
		switch c.Tag {
		case 0:
			name = string(c.Data.Bytes())
		case 1:
			value = c.Data.Bytes()
		}
	}
	if name == "" {
		return "", nil, errors.New("extended: no request name")
	}

	return name, value, nil
}

// envelope is an LDAPMessage around an operation.
func envelope(id int64, op *ber.Packet, controls []*ber.Packet) *ber.Packet {
	p := ber.NewSequence("LDAPMessage")
	p.AppendChild(ber.NewInteger(ber.ClassUniversal, ber.TypePrimitive, ber.TagInteger, id, "messageID"))
	p.AppendChild(op)
	if len(controls) > 0 {
		cs := ber.Encode(ber.ClassContext, ber.TypeConstructed, 0, nil, "controls")
		for _, c := range controls {
			cs.AppendChild(c)
		}
		p.AppendChild(cs)
	}

	return p
}

// encodeResult is an LDAPResult under an application tag.
func encodeResult(tag ber.Tag, r Result) *ber.Packet {
	p := ber.Encode(ber.ClassApplication, ber.TypeConstructed, tag, nil, "")
	appendResult(p, r)

	return p
}

func appendResult(p *ber.Packet, r Result) {
	p.AppendChild(ber.NewInteger(ber.ClassUniversal, ber.TypePrimitive, ber.TagEnumerated, r.Code, "resultCode"))
	p.AppendChild(ber.NewString(ber.ClassUniversal, ber.TypePrimitive, ber.TagOctetString, r.MatchedDN, "matchedDN"))
	p.AppendChild(ber.NewString(ber.ClassUniversal, ber.TypePrimitive, ber.TagOctetString, r.Message, "diagnosticMessage"))
}

func encodeExtended(r Result, name string, value []byte) *ber.Packet {
	p := encodeResult(tagExtendedResponse, r)
	if name != "" {
		p.AppendChild(ber.NewString(ber.ClassContext, ber.TypePrimitive, 10, name, "responseName"))
	}
	if value != nil {
		p.AppendChild(ber.NewString(ber.ClassContext, ber.TypePrimitive, 11, string(value), "responseValue"))
	}

	return p
}

func encodeEntry(e Entry) *ber.Packet {
	p := ber.Encode(ber.ClassApplication, ber.TypeConstructed, tagSearchResultEnt, nil, "SearchResultEntry")
	p.AppendChild(ber.NewString(ber.ClassUniversal, ber.TypePrimitive, ber.TagOctetString, e.DN, "objectName"))
	attrs := ber.NewSequence("attributes")
	for _, a := range e.Attributes {
		attr := ber.NewSequence("PartialAttribute")
		attr.AppendChild(ber.NewString(ber.ClassUniversal, ber.TypePrimitive, ber.TagOctetString, a.Name, "type"))
		vals := ber.Encode(ber.ClassUniversal, ber.TypeConstructed, ber.TagSet, nil, "vals")
		for _, v := range a.Values {
			vals.AppendChild(ber.NewString(ber.ClassUniversal, ber.TypePrimitive, ber.TagOctetString, v, ""))
		}
		attr.AppendChild(vals)
		attrs.AppendChild(attr)
	}
	p.AppendChild(attrs)

	return p
}

// encodePaging is the paged results control on a response: size zero (the
// server does not estimate) and the cookie of the next page.
func encodePaging(pg Paging) *ber.Packet {
	v := ber.NewSequence("realSearchControlValue")
	v.AppendChild(ber.NewInteger(ber.ClassUniversal, ber.TypePrimitive, ber.TagInteger, pg.Size, "size"))
	v.AppendChild(ber.NewString(ber.ClassUniversal, ber.TypePrimitive, ber.TagOctetString, string(pg.Cookie), "cookie"))

	c := ber.NewSequence("Control")
	c.AppendChild(ber.NewString(ber.ClassUniversal, ber.TypePrimitive, ber.TagOctetString, OidPagedResults, "controlType"))
	c.AppendChild(ber.NewString(ber.ClassUniversal, ber.TypePrimitive, ber.TagOctetString, string(v.Bytes()), "controlValue"))

	return c
}
