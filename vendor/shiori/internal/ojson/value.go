// Package ojson is an order-preserving JSON value model with an
// ECMAScript-compatible serializer.
//
// Artifacts must round-trip unknown members exactly and every hash/cursor
// preimage must match JavaScript's JSON.stringify output. encoding/json
// cannot guarantee either: it reorders map keys, collapses duplicate keys,
// loses number spelling and escapes <, >, & and U+2028/U+2029. This package
// keeps object members in source order, records duplicate member names
// instead of collapsing them, keeps the raw spelling of every number
// literal, and serializes strings with exactly the JSON.stringify escaping
// rules.
package ojson

import (
	"strconv"
	"unicode/utf8"
)

// Kind identifies the JSON type of a Value.
type Kind uint8

const (
	// Undefined is the zero Kind; it marks an absent member (JavaScript
	// "undefined"). It is never produced by the parser for a present value.
	Undefined Kind = iota
	Null
	Bool
	Number
	String
	Array
	Object
)

// Value is an immutable-by-convention JSON value.
type Value struct {
	kind Kind
	b    bool
	s    string // string contents, or the raw number literal
	arr  *[]Value
	obj  *[]Member
}

// Member is one object member. Order is significant.
type Member struct {
	Key   string
	Value Value
}

// Constructors.

func NullValue() Value           { return Value{kind: Null} }
func BoolValue(b bool) Value     { return Value{kind: Bool, b: b} }
func StringValue(s string) Value { return Value{kind: String, s: s} }
func IntValue(n int64) Value     { return Value{kind: Number, s: strconv.FormatInt(n, 10)} }

// RawNumber returns a number value with the given literal spelling. The
// caller must pass a valid JSON number literal.
func RawNumber(lit string) Value { return Value{kind: Number, s: lit} }

// ArrayValue wraps elements (the slice is retained).
func ArrayValue(elems []Value) Value {
	if elems == nil {
		elems = []Value{}
	}
	return Value{kind: Array, arr: &elems}
}

// ObjectValue wraps members (the slice is retained).
func ObjectValue(members []Member) Value {
	if members == nil {
		members = []Member{}
	}
	return Value{kind: Object, obj: &members}
}

// StringsValue builds an array of strings.
func StringsValue(ss []string) Value {
	out := make([]Value, len(ss))
	for i, s := range ss {
		out[i] = StringValue(s)
	}
	return ArrayValue(out)
}

// NullableString returns null when p is nil.
func NullableString(p *string) Value {
	if p == nil {
		return NullValue()
	}
	return StringValue(*p)
}

// Accessors.

func (v Value) Kind() Kind  { return v.kind }
func (v Value) Bool() bool  { return v.b }
func (v Value) Str() string { return v.s }
func (v Value) NumberLiteral() string {
	if v.kind != Number {
		return ""
	}
	return v.s
}

// Elems returns array elements (nil for non-arrays).
func (v Value) Elems() []Value {
	if v.arr == nil {
		return nil
	}
	return *v.arr
}

// Members returns object members in source order (nil for non-objects).
func (v Value) Members() []Member {
	if v.obj == nil {
		return nil
	}
	return *v.obj
}

// Float returns the IEEE-754 double JavaScript would parse from the literal.
func (v Value) Float() (float64, bool) {
	if v.kind != Number {
		return 0, false
	}
	f, err := strconv.ParseFloat(v.s, 64)
	if err != nil {
		// Out-of-range literals parse to ±Inf in JavaScript.
		if ne, ok := err.(*strconv.NumError); ok && ne.Err == strconv.ErrRange {
			return f, true
		}
		return 0, false
	}
	return f, true
}

// Get returns the value of the LAST member named key (JavaScript
// JSON.parse semantics for duplicate names) and whether it exists.
func (v Value) Get(key string) (Value, bool) {
	if v.kind != Object {
		return Value{}, false
	}
	ms := *v.obj
	for i := len(ms) - 1; i >= 0; i-- {
		if ms[i].Key == key {
			return ms[i].Value, true
		}
	}
	return Value{}, false
}

// TypeName is the zod/typeof-style type name used in "received X" messages.
func (v Value) TypeName() string {
	switch v.kind {
	case Undefined:
		return "undefined"
	case Null:
		return "null"
	case Bool:
		return "boolean"
	case Number:
		return "number"
	case String:
		return "string"
	case Array:
		return "array"
	default:
		return "object"
	}
}

// UniqueMembers returns the object members with duplicate names collapsed
// to the last occurrence, at the position of the FIRST occurrence (the
// JavaScript JSON.parse object-construction order).
func (v Value) UniqueMembers() []Member {
	if v.kind != Object {
		return nil
	}
	ms := *v.obj
	if !hasDuplicateKeys(ms) {
		return ms
	}
	pos := make(map[string]int, len(ms))
	out := make([]Member, 0, len(ms))
	for _, m := range ms {
		if i, ok := pos[m.Key]; ok {
			out[i].Value = m.Value
			continue
		}
		pos[m.Key] = len(out)
		out = append(out, m)
	}
	return out
}

// hasDuplicateKeys reports repeated member names (quadratic for small
// objects, which is cheaper than allocating a map).
func hasDuplicateKeys(ms []Member) bool {
	if len(ms) > 24 {
		seen := make(map[string]struct{}, len(ms))
		for _, m := range ms {
			if _, ok := seen[m.Key]; ok {
				return true
			}
			seen[m.Key] = struct{}{}
		}
		return false
	}
	for i := 1; i < len(ms); i++ {
		for j := 0; j < i; j++ {
			if ms[i].Key == ms[j].Key {
				return true
			}
		}
	}
	return false
}

// UTF16Len is the JavaScript string.length of s.
func UTF16Len(s string) int {
	n := 0
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
		i += size
	}
	return n
}

// CompareUTF16 compares a and b by UTF-16 code units, like JavaScript's
// default string comparison (not byte order, not code point order).
func CompareUTF16(a, b string) int {
	i, j := 0, 0
	var pendA, pendB rune = -1, -1 // pending low surrogate
	for {
		var ua, ub rune
		endA, endB := false, false
		if pendA >= 0 {
			ua, pendA = pendA, -1
		} else if i < len(a) {
			r, size := utf8.DecodeRuneInString(a[i:])
			i += size
			if r >= 0x10000 {
				r -= 0x10000
				ua = 0xD800 + (r >> 10)
				pendA = 0xDC00 + (r & 0x3FF)
			} else {
				ua = r
			}
		} else {
			endA = true
		}
		if pendB >= 0 {
			ub, pendB = pendB, -1
		} else if j < len(b) {
			r, size := utf8.DecodeRuneInString(b[j:])
			j += size
			if r >= 0x10000 {
				r -= 0x10000
				ub = 0xD800 + (r >> 10)
				pendB = 0xDC00 + (r & 0x3FF)
			} else {
				ub = r
			}
		} else {
			endB = true
		}
		switch {
		case endA && endB:
			return 0
		case endA:
			return -1
		case endB:
			return 1
		case ua < ub:
			return -1
		case ua > ub:
			return 1
		}
	}
}

// TruncateUTF16 returns s unchanged when its UTF-16 length is at most max.
// Otherwise it keeps the first max-1 code units (never splitting a
// surrogate pair) and appends U+2026. max < 1 is treated as 1.
func TruncateUTF16(s string, max int) (string, bool) {
	if max < 1 {
		max = 1
	}
	if UTF16Len(s) <= max {
		return s, false
	}
	keep := max - 1
	n := 0
	end := 0
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		w := 1
		if r >= 0x10000 {
			w = 2
		}
		if n+w > keep {
			break
		}
		n += w
		i += size
		end = i
	}
	return s[:end] + "…", true
}
