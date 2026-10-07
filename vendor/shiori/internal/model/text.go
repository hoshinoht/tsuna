// Package model holds the compatible decoders and structural rules for
// workplan artifacts (schema/v1). Decoding reproduces the reference
// validator's issue text and field paths.
package model

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
)

// IsECMAScriptSpace reports whether r is in the ECMAScript whitespace set
// used by String.prototype.trim (common.schema.json#/$defs/nonblank).
// Go's unicode.IsSpace and regexp \s differ from this set.
func IsECMAScriptSpace(r rune) bool {
	switch {
	case r >= 0x09 && r <= 0x0D, r == 0x20, r == 0xA0, r == 0x1680,
		r >= 0x2000 && r <= 0x200A, r == 0x2028, r == 0x2029, r == 0x202F,
		r == 0x205F, r == 0x3000, r == 0xFEFF:
		return true
	}
	return false
}

// TrimJS trims the ECMAScript whitespace set from both ends.
func TrimJS(s string) string { return strings.TrimFunc(s, IsECMAScriptSpace) }

// Blank reports whether s is empty after TrimJS.
func Blank(s string) bool { return TrimJS(s) == "" }

// ErrUnnormalizableID is the reference text for an id without [a-z0-9].
var ErrUnnormalizableID = errors.New("Workplan id must contain at least one letter or number")

// NormalizeID applies the workplan id normalization
// (common.schema.json#/$defs/workplanId x-shiori-normalization): trim;
// lowercase; replace each maximal run outside [a-z0-9] with '-'; strip
// leading/trailing '-'; truncate to 80 UTF-16 code units.
func NormalizeID(raw string) (string, error) {
	s := jsLower(TrimJS(raw))
	var b strings.Builder
	dash := false
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteRune(r)
			continue
		}
		dash = true
	}
	out := b.String()
	if len(out) > 80 { // ASCII only, so bytes == UTF-16 units
		out = out[:80]
	}
	if out == "" {
		return "", ErrUnnormalizableID
	}
	return out, nil
}

// jsLower approximates String.prototype.toLowerCase for the purpose of id
// normalization. Only the result's [a-z0-9] content matters, and the one
// relevant difference from strings.ToLower is U+0130, which JavaScript
// lowercases to "i̇".
func jsLower(s string) string {
	if strings.ContainsRune(s, 0x130) {
		s = strings.ReplaceAll(s, "İ", "i̇")
	}
	return strings.ToLower(s)
}

var isoDatetime = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})(?::(\d{2})(?:\.\d+)?)?Z$`)

// ValidDatetime implements common.schema.json#/$defs/isoDatetimeUtc plus the
// calendar check.
func ValidDatetime(s string) bool {
	m := isoDatetime.FindStringSubmatch(s)
	if m == nil {
		return false
	}
	year, _ := strconv.Atoi(m[1])
	month, _ := strconv.Atoi(m[2])
	day, _ := strconv.Atoi(m[3])
	hour, _ := strconv.Atoi(m[4])
	minute, _ := strconv.Atoi(m[5])
	if month < 1 || month > 12 || day < 1 || hour > 23 || minute > 59 {
		return false
	}
	if m[6] != "" {
		if sec, _ := strconv.Atoi(m[6]); sec > 59 {
			return false
		}
	}
	dim := [...]int{31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31}[month-1]
	if month == 2 && (year%4 == 0 && (year%100 != 0 || year%400 == 0)) {
		dim = 29
	}
	return day <= dim
}
