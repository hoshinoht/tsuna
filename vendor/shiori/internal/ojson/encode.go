package ojson

import "unicode/utf8"

const hexDigits = "0123456789abcdef"

// AppendQuoted appends s quoted with ECMAScript JSON.stringify escaping:
// '"' and '\\' are escaped; U+0008, U+0009, U+000A, U+000C, U+000D use the
// short forms; every other code point below U+0020 uses \u00xx with
// lowercase hex; everything else (including <, >, &, U+007F, U+2028,
// U+2029 and non-BMP characters) is emitted raw as UTF-8.
func AppendQuoted(dst []byte, s string) []byte {
	dst = append(dst, '"')
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 0x20 && c != '"' && c != '\\' {
			continue
		}
		dst = append(dst, s[start:i]...)
		switch c {
		case '"':
			dst = append(dst, '\\', '"')
		case '\\':
			dst = append(dst, '\\', '\\')
		case '\b':
			dst = append(dst, '\\', 'b')
		case '\t':
			dst = append(dst, '\\', 't')
		case '\n':
			dst = append(dst, '\\', 'n')
		case '\f':
			dst = append(dst, '\\', 'f')
		case '\r':
			dst = append(dst, '\\', 'r')
		default:
			dst = append(dst, '\\', 'u', '0', '0', hexDigits[c>>4], hexDigits[c&0xF])
		}
		start = i + 1
	}
	dst = append(dst, s[start:]...)
	return append(dst, '"')
}

// Quote returns the JSON.stringify quoting of s.
func Quote(s string) string { return string(AppendQuoted(nil, s)) }

// Compact serializes v like JSON.stringify(v).
func Compact(v Value) []byte { return AppendCompact(nil, v) }

// AppendCompact appends the compact serialization of v.
func AppendCompact(dst []byte, v Value) []byte {
	switch v.kind {
	case Null, Undefined:
		return append(dst, "null"...)
	case Bool:
		if v.b {
			return append(dst, "true"...)
		}
		return append(dst, "false"...)
	case Number:
		return append(dst, v.s...)
	case String:
		return AppendQuoted(dst, v.s)
	case Array:
		dst = append(dst, '[')
		for i, e := range v.Elems() {
			if i > 0 {
				dst = append(dst, ',')
			}
			dst = AppendCompact(dst, e)
		}
		return append(dst, ']')
	default:
		dst = append(dst, '{')
		first := true
		for _, m := range v.Members() {
			if m.Value.kind == Undefined {
				continue
			}
			if !first {
				dst = append(dst, ',')
			}
			first = false
			dst = AppendQuoted(dst, m.Key)
			dst = append(dst, ':')
			dst = AppendCompact(dst, m.Value)
		}
		return append(dst, '}')
	}
}

// Pretty serializes v like JSON.stringify(v, null, 2).
func Pretty(v Value) []byte {
	return appendPretty(nil, v, 0)
}

func appendIndent(dst []byte, n int) []byte {
	dst = append(dst, '\n')
	for i := 0; i < n; i++ {
		dst = append(dst, ' ', ' ')
	}
	return dst
}

func appendPretty(dst []byte, v Value, level int) []byte {
	switch v.kind {
	case Array:
		arr := v.Elems()
		if len(arr) == 0 {
			return append(dst, '[', ']')
		}
		dst = append(dst, '[')
		for i, e := range arr {
			if i > 0 {
				dst = append(dst, ',')
			}
			dst = appendIndent(dst, level+1)
			dst = appendPretty(dst, e, level+1)
		}
		dst = appendIndent(dst, level)
		return append(dst, ']')
	case Object:
		ms := v.Members()
		n := 0
		for _, m := range ms {
			if m.Value.kind != Undefined {
				n++
			}
		}
		if n == 0 {
			return append(dst, '{', '}')
		}
		dst = append(dst, '{')
		first := true
		for _, m := range ms {
			if m.Value.kind == Undefined {
				continue
			}
			if !first {
				dst = append(dst, ',')
			}
			first = false
			dst = appendIndent(dst, level+1)
			dst = AppendQuoted(dst, m.Key)
			dst = append(dst, ':', ' ')
			dst = appendPretty(dst, m.Value, level+1)
		}
		dst = appendIndent(dst, level)
		return append(dst, '}')
	default:
		return AppendCompact(dst, v)
	}
}

// UTF16LenBytes is UTF16Len for a byte slice.
func UTF16LenBytes(b []byte) int {
	n := 0
	for i := 0; i < len(b); {
		r, size := utf8.DecodeRune(b[i:])
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
		i += size
	}
	return n
}

// Builder is a small helper for constructing ordered objects.
type Builder struct{ members []Member }

// NewObject starts an ordered object with a capacity hint.
func NewObject(capHint int) *Builder { return &Builder{members: make([]Member, 0, capHint)} }

// Set appends a member (it does not replace an existing one).
func (b *Builder) Set(key string, v Value) *Builder {
	b.members = append(b.members, Member{Key: key, Value: v})
	return b
}

// Value finalizes the object.
func (b *Builder) Value() Value { return ObjectValue(b.members) }
