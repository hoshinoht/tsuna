package resume

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"regexp"

	"github.com/hoshinoht/shiori/internal/ojson"
)

// Cursor domains and errors (cursors-v1.schema.json).
const (
	inspectDomain = "workplan-inspect-cursor-v1"
	resumeDomain  = "workplan-resume-cursor-v1"
	// MaxCursorBytes bounds a cursor token.
	MaxCursorBytes = 4096
)

var (
	errInspectCursorInvalid = errors.New("Invalid workplan inspect cursor; restart without a cursor")
	errResumeCursorInvalid  = errors.New("Invalid workplan resume cursor; restart without a cursor")
	hashRE                  = regexp.MustCompile(`^[a-f0-9]{64}$`)

	// ErrInspectCursorStale refuses an inspect cursor for another state or
	// other options.
	ErrInspectCursorStale = errors.New("Stale or option-mismatched workplan inspect cursor; restart from the first page")
	// ErrCursorStale refuses a resume cursor for another state or other
	// options.
	ErrCursorStale = errors.New("Stale or option-mismatched workplan resume cursor; restart from the first page")
)

// cursorField is one payload member (key order is the checksum preimage).
type cursorField struct {
	Key   string
	Value ojson.Value
}

func payloadValue(fields []cursorField, checksum string) ojson.Value {
	b := ojson.NewObject(len(fields) + 1)
	for _, f := range fields {
		b.Set(f.Key, f.Value)
	}
	if checksum != "" {
		b.Set("checksum", ojson.StringValue(checksum))
	}
	return b.Value()
}

func checksum(domain string, fields []cursorField) string {
	h := sha256.New()
	h.Write([]byte(domain + "\n"))
	h.Write(ojson.Compact(payloadValue(fields, "")))
	return hex.EncodeToString(h.Sum(nil))
}

// encodeCursor returns the unpadded base64url token.
func encodeCursor(domain string, fields []cursorField) string {
	return base64.RawURLEncoding.EncodeToString(ojson.Compact(payloadValue(fields, checksum(domain, fields))))
}

// decodeCursor parses a token and verifies shape and checksum. keys lists
// the payload keys (without checksum) in preimage order. It returns the
// fields or ok=false for any undecodable/forged token.
func decodeCursor(domain, token string, keys []string) (map[string]ojson.Value, bool) {
	if token == "" || len(token) > MaxCursorBytes {
		return nil, false
	}
	// Only the unpadded base64url alphabet; Go's decoder would otherwise
	// skip CR/LF and let distinct tokens alias one position.
	for i := 0; i < len(token); i++ {
		c := token[i]
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return nil, false
		}
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil {
		return nil, false
	}
	parsed, err := ojson.Parse(raw)
	if err != nil || len(parsed.Duplicates) > 0 || parsed.Value.Kind() != ojson.Object {
		return nil, false
	}
	ms := parsed.Value.Members()
	if len(ms) != len(keys)+1 {
		return nil, false
	}
	fields := make([]cursorField, len(keys))
	out := make(map[string]ojson.Value, len(keys))
	for i, k := range keys {
		if ms[i].Key != k {
			return nil, false
		}
		fields[i] = cursorField{k, ms[i].Value}
		out[k] = ms[i].Value
	}
	last := ms[len(keys)]
	if last.Key != "checksum" || last.Value.Kind() != ojson.String || !hashRE.MatchString(last.Value.Str()) {
		return nil, false
	}
	if checksum(domain, fields) != last.Value.Str() {
		return nil, false
	}
	return out, true
}

// intField extracts a non-negative safe integer.
func intField(v ojson.Value) (int, bool) {
	f, ok := v.Float()
	if !ok || f < 0 || f != float64(int64(f)) || f > 1<<53 {
		return 0, false
	}
	return int(f), true
}

// nullableStringField extracts a string or null.
func nullableStringField(v ojson.Value) (*string, bool) {
	switch v.Kind() {
	case ojson.Null:
		return nil, true
	case ojson.String:
		s := v.Str()
		return &s, true
	}
	return nil, false
}

// EqualPtr reports whether two optional strings are both absent or equal.
func EqualPtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func ptrValue(p *string) ojson.Value { return ojson.NullableString(p) }

// InspectCursor is the position of a workplan_inspect page.
type InspectCursor struct {
	StateHash string
	PhaseID   *string
	Limit     int
	Offset    int
}

var inspectKeys = []string{"version", "stateHash", "phaseId", "limit", "offset"}

func (c InspectCursor) fields() []cursorField {
	return []cursorField{
		{"version", ojson.IntValue(1)},
		{"stateHash", ojson.StringValue(c.StateHash)},
		{"phaseId", ptrValue(c.PhaseID)},
		{"limit", ojson.IntValue(int64(c.Limit))},
		{"offset", ojson.IntValue(int64(c.Offset))},
	}
}

// Encode returns the cursor token.
func (c InspectCursor) Encode() string { return encodeCursor(inspectDomain, c.fields()) }

// ParseInspectCursor decodes and verifies an inspect cursor token.
func ParseInspectCursor(token string) (InspectCursor, error) {
	m, ok := decodeCursor(inspectDomain, token, inspectKeys)
	if !ok {
		return InspectCursor{}, errInspectCursorInvalid
	}
	var c InspectCursor
	v, ok1 := intField(m["version"])
	c.Limit, ok = intField(m["limit"])
	off, ok3 := intField(m["offset"])
	pid, ok4 := nullableStringField(m["phaseId"])
	sh := m["stateHash"]
	if !ok || !ok1 || v != 1 || !ok3 || !ok4 || sh.Kind() != ojson.String || !hashRE.MatchString(sh.Str()) {
		return InspectCursor{}, errInspectCursorInvalid
	}
	c.Offset, c.PhaseID, c.StateHash = off, pid, sh.Str()
	return c, nil
}

// Cursor is the position of a workplan_resume page. It holds the raw
// filter ids when encoding; after decoding, PhaseID/StepID hold the stored
// digests (compare with FilterDigest).
type Cursor struct {
	StateHash string
	MaxChars  int
	Limit     int
	PhaseID   *string
	StepID    *string
	Offset    int
}

var resumeKeys = []string{"version", "stateHash", "maxChars", "limit", "phaseId", "stepId", "offset"}

// FilterDigest is how a resume cursor binds a phase/step filter: the
// reference stores "sha256:<hex of the id>" rather than the id itself.
func FilterDigest(p *string) *string {
	if p == nil {
		return nil
	}
	sum := sha256.Sum256([]byte(*p))
	d := "sha256:" + hex.EncodeToString(sum[:])
	return &d
}

func (c Cursor) fields() []cursorField {
	return []cursorField{
		{"version", ojson.IntValue(1)},
		{"stateHash", ojson.StringValue(c.StateHash)},
		{"maxChars", ojson.IntValue(int64(c.MaxChars))},
		{"limit", ojson.IntValue(int64(c.Limit))},
		{"phaseId", ptrValue(FilterDigest(c.PhaseID))},
		{"stepId", ptrValue(FilterDigest(c.StepID))},
		{"offset", ojson.IntValue(int64(c.Offset))},
	}
}

// Encode returns the cursor token.
func (c Cursor) Encode() string { return encodeCursor(resumeDomain, c.fields()) }

// ParseCursor decodes and verifies a resume cursor token.
func ParseCursor(token string) (Cursor, error) {
	m, ok := decodeCursor(resumeDomain, token, resumeKeys)
	if !ok {
		return Cursor{}, errResumeCursorInvalid
	}
	var c Cursor
	v, ok1 := intField(m["version"])
	mc, ok2 := intField(m["maxChars"])
	lim, ok3 := intField(m["limit"])
	off, ok4 := intField(m["offset"])
	pid, ok5 := nullableStringField(m["phaseId"])
	sid, ok6 := nullableStringField(m["stepId"])
	sh := m["stateHash"]
	if !ok1 || v != 1 || !ok2 || !ok3 || !ok4 || !ok5 || !ok6 || sh.Kind() != ojson.String || !hashRE.MatchString(sh.Str()) {
		return Cursor{}, errResumeCursorInvalid
	}
	c = Cursor{StateHash: sh.Str(), MaxChars: mc, Limit: lim, PhaseID: pid, StepID: sid, Offset: off}
	return c, nil
}
