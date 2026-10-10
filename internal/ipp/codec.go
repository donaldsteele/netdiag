// Package ipp is a minimal, read-only IPP client (RFC 8010/8011). It only
// ever sends Get-Printer-Attributes and maps the reply onto
// model.PrinterInfo.
package ipp

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// Delimiter tags (RFC 8010 3.5.1).
const (
	TagOperation   byte = 0x01
	TagJob         byte = 0x02
	TagEnd         byte = 0x03
	TagPrinter     byte = 0x04
	TagUnsupported byte = 0x05
)

// Value tags (RFC 8010 3.5.2) used by this package.
const (
	TagUnsupportedValue byte = 0x10
	TagUnknown          byte = 0x12
	TagNoValue          byte = 0x13
	TagInteger          byte = 0x21
	TagBoolean          byte = 0x22
	TagEnum             byte = 0x23
	TagOctetString      byte = 0x30
	TagDateTime         byte = 0x31
	TagResolution       byte = 0x32
	TagRangeOfInteger   byte = 0x33
	TagBeginCollection  byte = 0x34
	TagTextWithLang     byte = 0x35
	TagNameWithLang     byte = 0x36
	TagEndCollection    byte = 0x37
	TagText             byte = 0x41
	TagName             byte = 0x42
	TagKeyword          byte = 0x44
	TagURI              byte = 0x45
	TagURIScheme        byte = 0x46
	TagCharset          byte = 0x47
	TagLanguage         byte = 0x48
	TagMimeType         byte = 0x49
	TagMemberName       byte = 0x4A
	tagExtension        byte = 0x7F
)

// OpGetPrinterAttributes is the only operation this package sends.
const OpGetPrinterAttributes uint16 = 0x000B

// Value is one attribute value. Integer, boolean and enum values are in Int;
// all string-like and octet values are in Str (for *WithLanguage tags only
// the text part).
type Value struct {
	Tag byte
	Int int32
	Str string
}

// Attribute is a named, possibly multi-valued attribute.
type Attribute struct {
	Name   string
	Values []Value
}

// Group is one attribute group (operation, printer, ...).
type Group struct {
	Tag   byte
	Attrs []Attribute
}

// Message is an IPP request or response. Code is the operation-id in a
// request and the status-code in a response.
type Message struct {
	Major, Minor uint8
	Code         uint16
	RequestID    uint32
	Groups       []Group
}

// Errors returned by Decode.
var (
	ErrShort = errors.New("ipp: truncated message")
	ErrNoEnd = errors.New("ipp: missing end-of-attributes tag")
)

// Limits on decoded data (the transport already caps the body at 64 KB).
const (
	maxAttrs  = 4096
	maxValues = 1024
	maxDepth  = 16
)

// Encode serializes m. Values are encoded according to their tag; additional
// values of a multi-valued attribute use an empty name.
func Encode(m *Message) []byte {
	b := make([]byte, 0, 256)
	b = append(b, m.Major, m.Minor)
	b = binary.BigEndian.AppendUint16(b, m.Code)
	b = binary.BigEndian.AppendUint32(b, m.RequestID)
	for _, g := range m.Groups {
		b = append(b, g.Tag)
		for _, a := range g.Attrs {
			for i, v := range a.Values {
				name := a.Name
				if i > 0 {
					name = ""
				}
				b = append(b, v.Tag)
				b = binary.BigEndian.AppendUint16(b, uint16(len(name)))
				b = append(b, name...)
				b = appendValue(b, v)
			}
		}
	}
	return append(b, TagEnd)
}

func appendValue(b []byte, v Value) []byte {
	switch v.Tag {
	case TagInteger, TagEnum:
		b = binary.BigEndian.AppendUint16(b, 4)
		return binary.BigEndian.AppendUint32(b, uint32(v.Int))
	case TagBoolean:
		x := byte(0)
		if v.Int != 0 {
			x = 1
		}
		return append(b, 0, 1, x)
	case TagTextWithLang, TagNameWithLang:
		lang := "en"
		if 4+len(lang)+len(v.Str) > 0xFFFF {
			lang = ""
		}
		b = binary.BigEndian.AppendUint16(b, uint16(4+len(lang)+len(v.Str)))
		b = binary.BigEndian.AppendUint16(b, uint16(len(lang)))
		b = append(b, lang...)
		b = binary.BigEndian.AppendUint16(b, uint16(len(v.Str)))
		return append(b, v.Str...)
	case TagUnsupportedValue, TagUnknown, TagNoValue, TagBeginCollection, TagEndCollection:
		return append(b, 0, 0)
	default:
		b = binary.BigEndian.AppendUint16(b, uint16(len(v.Str)))
		return append(b, v.Str...)
	}
}

// Decode parses an IPP message. On error it returns the message decoded so
// far (never nil) together with the error. Collections are skipped (their
// member values are not attached to any attribute) and unknown value tags
// are kept with their raw bytes in Str.
func Decode(b []byte) (*Message, error) {
	m := &Message{}
	if len(b) < 8 {
		return m, ErrShort
	}
	m.Major, m.Minor = b[0], b[1]
	m.Code = binary.BigEndian.Uint16(b[2:])
	m.RequestID = binary.BigEndian.Uint32(b[4:])
	p := 8
	var g *Group
	var cur *Attribute
	depth := 0
	nattrs := 0
	for {
		if p >= len(b) {
			return m, ErrNoEnd
		}
		tag := b[p]
		p++
		if tag == TagEnd {
			return m, nil
		}
		if tag < 0x10 { // delimiter: new group
			if depth != 0 {
				return m, fmt.Errorf("ipp: group tag 0x%02x inside collection", tag)
			}
			m.Groups = append(m.Groups, Group{Tag: tag})
			g = &m.Groups[len(m.Groups)-1]
			cur = nil
			continue
		}
		if tag == tagExtension {
			// The 4-byte extended tag lives in the value; treat as opaque.
			tag = TagUnknown
		}
		if p+2 > len(b) {
			return m, ErrShort
		}
		nl := int(binary.BigEndian.Uint16(b[p:]))
		p += 2
		if p+nl+2 > len(b) {
			return m, ErrShort
		}
		name := string(b[p : p+nl])
		p += nl
		vl := int(binary.BigEndian.Uint16(b[p:]))
		p += 2
		if p+vl > len(b) {
			return m, ErrShort
		}
		raw := b[p : p+vl]
		p += vl
		if g == nil {
			return m, errors.New("ipp: attribute before group tag")
		}
		switch {
		case tag == TagBeginCollection:
			if depth == 0 && nl > 0 {
				// Keep the attribute name so callers can see it exists.
				if nattrs++; nattrs > maxAttrs {
					return m, errors.New("ipp: too many attributes")
				}
				g.Attrs = append(g.Attrs, Attribute{Name: name})
				cur = nil
			}
			if depth++; depth > maxDepth {
				return m, errors.New("ipp: collections nested too deep")
			}
			continue
		case tag == TagEndCollection:
			if depth == 0 {
				return m, errors.New("ipp: unbalanced endCollection")
			}
			depth--
			continue
		case depth > 0:
			continue // member attributes of a collection are skipped
		}
		v, err := decodeValue(tag, raw)
		if err != nil {
			return m, fmt.Errorf("ipp: attribute %q: %w", name, err)
		}
		if nl == 0 {
			if cur == nil {
				return m, errors.New("ipp: additional value without attribute")
			}
			if len(cur.Values) < maxValues {
				cur.Values = append(cur.Values, v)
			}
			continue
		}
		if nattrs++; nattrs > maxAttrs {
			return m, errors.New("ipp: too many attributes")
		}
		g.Attrs = append(g.Attrs, Attribute{Name: name, Values: []Value{v}})
		cur = &g.Attrs[len(g.Attrs)-1]
	}
}

func decodeValue(tag byte, raw []byte) (Value, error) {
	v := Value{Tag: tag}
	switch tag {
	case TagInteger, TagEnum:
		if len(raw) != 4 {
			return v, fmt.Errorf("bad integer length %d", len(raw))
		}
		v.Int = int32(binary.BigEndian.Uint32(raw))
	case TagBoolean:
		if len(raw) != 1 {
			return v, fmt.Errorf("bad boolean length %d", len(raw))
		}
		v.Int = int32(raw[0])
	case TagTextWithLang, TagNameWithLang:
		if len(raw) < 2 {
			return v, ErrShort
		}
		ll := int(binary.BigEndian.Uint16(raw))
		if 2+ll+2 > len(raw) {
			return v, ErrShort
		}
		tl := int(binary.BigEndian.Uint16(raw[2+ll:]))
		if 4+ll+tl > len(raw) {
			return v, ErrShort
		}
		v.Str = string(raw[4+ll : 4+ll+tl])
	default:
		v.Str = string(raw)
	}
	return v, nil
}

// Attr finds the first attribute named name in groups with tag gtag.
func (m *Message) Attr(gtag byte, name string) *Attribute {
	for gi := range m.Groups {
		g := &m.Groups[gi]
		if g.Tag != gtag {
			continue
		}
		for ai := range g.Attrs {
			if g.Attrs[ai].Name == name {
				return &g.Attrs[ai]
			}
		}
	}
	return nil
}
