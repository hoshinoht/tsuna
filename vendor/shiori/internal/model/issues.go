package model

import (
	"strconv"
	"strings"

	"github.com/hoshinoht/shiori/internal/ojson"
)

// Issue is one decoding or validation problem at a field path.
type Issue struct {
	Path    []string
	Message string
}

// PathString joins the path with '.' (the reference's dot-joined form).
func (i Issue) PathString() string { return strings.Join(i.Path, ".") }

// String formats "path: message", using "$" for the root path.
func (i Issue) String() string {
	p := i.PathString()
	if p == "" {
		p = "$"
	}
	return p + ": " + i.Message
}

// JoinIssues formats issues joined with "; " (the reference's error text).
func JoinIssues(issues []Issue) string {
	parts := make([]string, len(issues))
	for i, is := range issues {
		parts[i] = is.String()
	}
	return strings.Join(parts, "; ")
}

// DecodeError is a failed compatible decode; Error() is the reference text.
type DecodeError struct{ Issues []Issue }

func (e *DecodeError) Error() string { return JoinIssues(e.Issues) }

// checker accumulates issues in schema property order.
type checker struct {
	issues []Issue
}

// fp is a lazily materialised field path: base plus an optional key.
// Building it costs nothing unless an issue is recorded.
type fp struct {
	base []string
	key  string
}

func at(base []string, key string) fp { return fp{base, key} }

func (f fp) slice() []string {
	n := len(f.base)
	if f.key != "" {
		n++
	}
	out := make([]string, 0, n)
	out = append(out, f.base...)
	if f.key != "" {
		out = append(out, f.key)
	}
	return out
}

func (c *checker) add(path fp, msg string) {
	c.issues = append(c.issues, Issue{Path: path.slice(), Message: msg})
}

func child(path []string, seg string) []string {
	out := make([]string, len(path)+1)
	copy(out, path)
	out[len(path)] = seg
	return out
}

func idx(path []string, i int) []string { return child(path, strconv.Itoa(i)) }

func expected(t string, v ojson.Value) string {
	return "Invalid input: expected " + t + ", received " + v.TypeName()
}

// str requires a string.
func (c *checker) str(path fp, v ojson.Value) (string, bool) {
	if v.Kind() != ojson.String {
		c.add(path, expected("string", v))
		return "", false
	}
	return v.Str(), true
}

// optStr accepts an absent member or a string.
func (c *checker) optStr(path fp, v ojson.Value, present bool) (*string, bool) {
	if !present {
		return nil, true
	}
	s, ok := c.str(path, v)
	if !ok {
		return nil, false
	}
	return &s, true
}

// optStrInto is optStr storing into a caller-provided slot (lets callers
// back several optional fields with one allocation).
func (c *checker) optStrInto(path fp, v ojson.Value, present bool, slot *string) *string {
	if !present {
		return nil
	}
	s, ok := c.str(path, v)
	if !ok {
		return nil
	}
	*slot = s
	return slot
}

// nullableStr accepts a string or null (absent is an error).
func (c *checker) nullableStr(path fp, v ojson.Value) (*string, bool) {
	if v.Kind() == ojson.Null {
		return nil, true
	}
	s, ok := c.str(path, v)
	if !ok {
		return nil, false
	}
	return &s, true
}

// nonblank requires a string that is nonempty after TrimJS.
func (c *checker) nonblank(path fp, v ojson.Value) (string, bool) {
	s, ok := c.str(path, v)
	if !ok {
		return "", false
	}
	if Blank(s) {
		c.add(path, "Too small: expected string to have >=1 characters")
		return "", false
	}
	return s, true
}

func (c *checker) strList(path fp, v ojson.Value) ([]string, bool) {
	if v.Kind() != ojson.Array {
		c.add(path, expected("array", v))
		return nil, false
	}
	out := make([]string, 0, len(v.Elems()))
	ok := true
	for i, e := range v.Elems() {
		if e.Kind() != ojson.String {
			c.add(fp{base: idx(path.slice(), i)}, expected("string", e))
			ok = false
			continue
		}
		out = append(out, e.Str())
	}
	return out, ok
}

func enumMessage(options []string) string {
	q := make([]string, len(options))
	for i, o := range options {
		q[i] = strconv.Quote(o)
	}
	return "Invalid option: expected one of " + strings.Join(q, "|")
}

func (c *checker) enum(path fp, v ojson.Value, options []string) (string, bool) {
	if v.Kind() == ojson.String {
		for _, o := range options {
			if v.Str() == o {
				return o, true
			}
		}
	}
	c.add(path, enumMessage(options))
	return "", false
}

func (c *checker) literalNumber(path fp, v ojson.Value, want float64) bool {
	if f, ok := v.Float(); ok && f == want {
		return true
	}
	c.add(path, "Invalid input: expected "+strconv.FormatFloat(want, 'f', -1, 64))
	return false
}

// object requires an object and returns its members (duplicates collapsed
// with JavaScript semantics).
func (c *checker) object(path fp, v ojson.Value) ([]ojson.Member, bool) {
	if v.Kind() != ojson.Object {
		c.add(path, expected("object", v))
		return nil, false
	}
	return v.UniqueMembers(), true
}

func (c *checker) array(path fp, v ojson.Value) ([]ojson.Value, bool) {
	if v.Kind() != ojson.Array {
		c.add(path, expected("array", v))
		return nil, false
	}
	return v.Elems(), true
}

// fields is a lookup over unique members. Objects are small, so a
// linear scan beats allocating a map per object.
type fields []ojson.Member

func fieldMap(ms []ojson.Member) fields { return fields(ms) }

func (f fields) get(k string) (ojson.Value, bool) {
	for i := range f {
		if f[i].Key == k {
			return f[i].Value, true
		}
	}
	return ojson.Value{}, false
}

// unknownMembers returns members whose key is not in known, in order.
func unknownMembers(ms []ojson.Member, known map[string]bool) []ojson.Member {
	var out []ojson.Member
	for _, m := range ms {
		if !known[m.Key] {
			out = append(out, m)
		}
	}
	return out
}

func set(keys ...string) map[string]bool {
	m := make(map[string]bool, len(keys))
	for _, k := range keys {
		m[k] = true
	}
	return m
}
