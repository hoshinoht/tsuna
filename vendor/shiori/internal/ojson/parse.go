package ojson

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
	"unsafe"
)

// MaxDepth is the default nesting limit.
const MaxDepth = 128

// SyntaxError describes malformed JSON. Its text deliberately differs from
// JavaScript engine text: comparators match only the
// stable prefix that callers put in front of it.
type SyntaxError struct {
	Offset int
	Msg    string
}

func (e *SyntaxError) Error() string {
	return fmt.Sprintf("JSON parse error: %s at byte %d", e.Msg, e.Offset)
}

// Duplicate records a repeated member name and the dotted path of the
// object that contains it ("" for the root object).
type Duplicate struct {
	Path string
	Key  string
}

// Parsed is the result of Parse.
type Parsed struct {
	Value      Value
	Duplicates []Duplicate
}

// Parse decodes exactly one JSON text. Invalid UTF-8 bytes and lone
// surrogate escapes are replaced with U+FFFD (TextDecoder behaviour).
func Parse(data []byte) (Parsed, error) {
	return parse(data, false)
}

// ParseImmutable is Parse without copying unescaped strings: returned
// strings alias data, so data must never be modified afterwards. Snapshot
// artifact buffers satisfy this (they are read once and never mutated).
func ParseImmutable(data []byte) (Parsed, error) {
	return parse(data, true)
}

func parse(data []byte, noCopy bool) (Parsed, error) {
	p := &parser{data: data, maxDepth: MaxDepth, noCopy: noCopy}
	p.skipWS()
	v, err := p.value(0)
	if err != nil {
		return Parsed{}, err
	}
	p.skipWS()
	if p.pos != len(p.data) {
		return Parsed{}, p.errf("unexpected data after top-level value")
	}
	return Parsed{Value: v, Duplicates: p.dups}, nil
}

type parser struct {
	data     []byte
	pos      int
	maxDepth int
	dups     []Duplicate
	// stack holds the path segments of the value being parsed; the
	// dotted path string is only built when a duplicate is recorded.
	stack []segment
	// scratch stacks for building arrays/objects without slice regrowth.
	elemScratch   []Value
	memberScratch []Member
	noCopy        bool
}

type segment struct {
	key   string
	index int
	isIdx bool
}

func (p *parser) pathString() string {
	var b []byte
	for i, sg := range p.stack {
		if i > 0 {
			b = append(b, '.')
		}
		if sg.isIdx {
			b = strconv.AppendInt(b, int64(sg.index), 10)
		} else {
			b = append(b, sg.key...)
		}
	}
	return string(b)
}

func (p *parser) errf(format string, a ...any) error {
	return &SyntaxError{Offset: p.pos, Msg: fmt.Sprintf(format, a...)}
}

func (p *parser) skipWS() {
	for p.pos < len(p.data) {
		switch p.data[p.pos] {
		case ' ', '\t', '\n', '\r':
			p.pos++
		default:
			return
		}
	}
}

func (p *parser) value(depth int) (Value, error) {
	if p.pos >= len(p.data) {
		return Value{}, p.errf("unexpected end of input")
	}
	switch c := p.data[p.pos]; {
	case c == '{':
		if depth >= p.maxDepth {
			return Value{}, p.errf("nesting exceeds %d levels", p.maxDepth)
		}
		return p.object(depth)
	case c == '[':
		if depth >= p.maxDepth {
			return Value{}, p.errf("nesting exceeds %d levels", p.maxDepth)
		}
		return p.array(depth)
	case c == '"':
		s, err := p.str()
		if err != nil {
			return Value{}, err
		}
		return StringValue(s), nil
	case c == 't':
		return p.literal("true", BoolValue(true))
	case c == 'f':
		return p.literal("false", BoolValue(false))
	case c == 'n':
		return p.literal("null", NullValue())
	case c == '-' || (c >= '0' && c <= '9'):
		return p.number()
	default:
		return Value{}, p.errf("unexpected character %s", quoteByte(c))
	}
}

func quoteByte(c byte) string {
	if c >= 0x20 && c < 0x7f && c != '"' && c != '\\' {
		return "'" + string(c) + "'"
	}
	return fmt.Sprintf("0x%02x", c)
}

func (p *parser) literal(word string, v Value) (Value, error) {
	if len(p.data)-p.pos < len(word) || string(p.data[p.pos:p.pos+len(word)]) != word {
		return Value{}, p.errf("invalid literal")
	}
	p.pos += len(word)
	return v, nil
}

func (p *parser) number() (Value, error) {
	start := p.pos
	if p.data[p.pos] == '-' {
		p.pos++
	}
	if p.pos >= len(p.data) {
		return Value{}, p.errf("unexpected end of input in number")
	}
	if p.data[p.pos] == '0' {
		p.pos++
	} else if p.data[p.pos] >= '1' && p.data[p.pos] <= '9' {
		for p.pos < len(p.data) && p.data[p.pos] >= '0' && p.data[p.pos] <= '9' {
			p.pos++
		}
	} else {
		return Value{}, p.errf("invalid number")
	}
	if p.pos < len(p.data) && p.data[p.pos] == '.' {
		p.pos++
		d := p.pos
		for p.pos < len(p.data) && p.data[p.pos] >= '0' && p.data[p.pos] <= '9' {
			p.pos++
		}
		if p.pos == d {
			return Value{}, p.errf("invalid number fraction")
		}
	}
	if p.pos < len(p.data) && (p.data[p.pos] == 'e' || p.data[p.pos] == 'E') {
		p.pos++
		if p.pos < len(p.data) && (p.data[p.pos] == '+' || p.data[p.pos] == '-') {
			p.pos++
		}
		d := p.pos
		for p.pos < len(p.data) && p.data[p.pos] >= '0' && p.data[p.pos] <= '9' {
			p.pos++
		}
		if p.pos == d {
			return Value{}, p.errf("invalid number exponent")
		}
	}
	return RawNumber(string(p.data[start:p.pos])), nil
}

func (p *parser) str() (string, error) {
	p.pos++ // opening quote
	var sb strings.Builder
	start := p.pos
	simple := true
	for {
		if p.pos >= len(p.data) {
			return "", p.errf("unterminated string")
		}
		c := p.data[p.pos]
		if c == '"' {
			if simple {
				raw := p.data[start:p.pos]
				p.pos++
				if !utf8.Valid(raw) {
					return strings.ToValidUTF8(string(raw), "�"), nil
				}
				if p.noCopy && len(raw) > 0 {
					// Zero-copy: the caller guarantees the input bytes are
					// immutable for the lifetime of the parsed value.
					return unsafe.String(&raw[0], len(raw)), nil
				}
				return string(raw), nil
			}
			p.pos++
			return sb.String(), nil
		}
		if c < 0x20 {
			return "", p.errf("unescaped control character in string")
		}
		if c == '\\' {
			if simple {
				sb.WriteString(strings.ToValidUTF8(string(p.data[start:p.pos]), "�"))
				simple = false
			}
			p.pos++
			if p.pos >= len(p.data) {
				return "", p.errf("unterminated escape")
			}
			e := p.data[p.pos]
			p.pos++
			switch e {
			case '"', '\\', '/':
				sb.WriteByte(e)
			case 'b':
				sb.WriteByte('\b')
			case 'f':
				sb.WriteByte('\f')
			case 'n':
				sb.WriteByte('\n')
			case 'r':
				sb.WriteByte('\r')
			case 't':
				sb.WriteByte('\t')
			case 'u':
				r, err := p.hex4()
				if err != nil {
					return "", err
				}
				if utf16.IsSurrogate(r) {
					if r < 0xDC00 && p.pos+6 <= len(p.data) && p.data[p.pos] == '\\' && p.data[p.pos+1] == 'u' {
						save := p.pos
						p.pos += 2
						r2, err := p.hex4()
						if err == nil && r2 >= 0xDC00 && r2 <= 0xDFFF {
							sb.WriteRune(utf16.DecodeRune(r, r2))
							continue
						}
						p.pos = save
					}
					sb.WriteRune(utf8.RuneError)
					continue
				}
				sb.WriteRune(r)
			default:
				p.pos--
				return "", p.errf("invalid escape")
			}
			continue
		}
		if simple {
			p.pos++
			continue
		}
		// Copy one UTF-8 sequence (invalid bytes become U+FFFD).
		r, size := utf8.DecodeRune(p.data[p.pos:])
		if r == utf8.RuneError && size <= 1 {
			sb.WriteRune(utf8.RuneError)
			p.pos++
			continue
		}
		sb.Write(p.data[p.pos : p.pos+size])
		p.pos += size
	}
}

func (p *parser) hex4() (rune, error) {
	if p.pos+4 > len(p.data) {
		return 0, p.errf("invalid unicode escape")
	}
	n, err := strconv.ParseUint(string(p.data[p.pos:p.pos+4]), 16, 32)
	if err != nil {
		return 0, p.errf("invalid unicode escape")
	}
	p.pos += 4
	return rune(n), nil
}

func (p *parser) array(depth int) (Value, error) {
	p.pos++
	p.skipWS()
	if p.pos < len(p.data) && p.data[p.pos] == ']' {
		p.pos++
		return ArrayValue([]Value{}), nil
	}
	base := len(p.elemScratch)
	defer func() { p.elemScratch = p.elemScratch[:base] }()
	for {
		p.skipWS()
		p.stack = append(p.stack, segment{index: len(p.elemScratch) - base, isIdx: true})
		v, err := p.value(depth + 1)
		p.stack = p.stack[:len(p.stack)-1]
		if err != nil {
			return Value{}, err
		}
		p.elemScratch = append(p.elemScratch, v)
		p.skipWS()
		if p.pos >= len(p.data) {
			return Value{}, p.errf("unexpected end of input in array")
		}
		switch p.data[p.pos] {
		case ',':
			p.pos++
		case ']':
			p.pos++
			elems := make([]Value, len(p.elemScratch)-base)
			copy(elems, p.elemScratch[base:])
			return ArrayValue(elems), nil
		default:
			return Value{}, p.errf("expected ',' or ']'")
		}
	}
}

func (p *parser) object(depth int) (Value, error) {
	p.pos++
	p.skipWS()
	if p.pos < len(p.data) && p.data[p.pos] == '}' {
		p.pos++
		return ObjectValue([]Member{}), nil
	}
	base := len(p.memberScratch)
	defer func() { p.memberScratch = p.memberScratch[:base] }()
	var seen map[string]struct{}
	for {
		p.skipWS()
		if p.pos >= len(p.data) {
			return Value{}, p.errf("unexpected end of input in object")
		}
		if p.data[p.pos] != '"' {
			return Value{}, p.errf("expected member name")
		}
		key, err := p.str()
		if err != nil {
			return Value{}, err
		}
		p.skipWS()
		if p.pos >= len(p.data) || p.data[p.pos] != ':' {
			return Value{}, p.errf("expected ':' after member name")
		}
		p.pos++
		p.skipWS()
		p.stack = append(p.stack, segment{key: key})
		v, err := p.value(depth + 1)
		p.stack = p.stack[:len(p.stack)-1]
		if err != nil {
			return Value{}, err
		}
		members := p.memberScratch[base:]
		dup := false
		if seen != nil {
			_, dup = seen[key]
			seen[key] = struct{}{}
		} else {
			for i := range members {
				if members[i].Key == key {
					dup = true
					break
				}
			}
			if len(members) >= 24 {
				seen = make(map[string]struct{}, 2*len(members))
				for i := range members {
					seen[members[i].Key] = struct{}{}
				}
				seen[key] = struct{}{}
			}
		}
		if dup {
			p.dups = append(p.dups, Duplicate{Path: p.pathString(), Key: key})
		}
		p.memberScratch = append(p.memberScratch, Member{Key: key, Value: v})
		p.skipWS()
		if p.pos >= len(p.data) {
			return Value{}, p.errf("unexpected end of input in object")
		}
		switch p.data[p.pos] {
		case ',':
			p.pos++
		case '}':
			p.pos++
			out := make([]Member, len(p.memberScratch)-base)
			copy(out, p.memberScratch[base:])
			return ObjectValue(out), nil
		default:
			return Value{}, p.errf("expected ',' or '}'")
		}
	}
}
