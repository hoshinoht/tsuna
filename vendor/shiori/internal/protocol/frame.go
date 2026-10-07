// Package protocol implements `shiori serve --stdio`: the bounded,
// versioned JSON-lines protocol between the Go core and a native host
// adapter (protocol-envelope-v1).
//
// stdout carries protocol frames only; diagnostics go to stderr with
// redaction. Every mutating tool request is prepared first and touches
// nothing; only shiori.commit of that exact, unchanged, single-use intent
// writes, and only on the connection that prepared it.
package protocol

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/hoshinoht/shiori/internal/ojson"
)

// ProtocolVersion is the only protocol version this core speaks.
const ProtocolVersion = 1

// Default limits.
const (
	DefaultMaxFrameBytes    = 16 << 20
	DefaultMaxResponseBytes = 64 << 20
)

var requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

// ToolOperations are the thirteen retained native identities.
var ToolOperations = []string{
	"workplan_create", "workplan_update", "workplan_patch", "workplan_reset",
	"workplan_read", "workplan_list", "workplan_inspect", "workplan_validate",
	"workplan_checkpoint", "workplan_resume", "workplan_compact", "workplan_doctor",
	"workplan_compact_preview",
}

// ControlOperations are the protocol control methods.
var ControlOperations = []string{"shiori.handshake", "shiori.commit", "shiori.discard"}

func knownOperation(op string) bool {
	for _, o := range ToolOperations {
		if o == op {
			return true
		}
	}
	for _, o := range ControlOperations {
		if o == op {
			return true
		}
	}
	return false
}

// frameError is a malformed or disallowed frame. The connection is closed
// after it is reported (fail closed: every prepared intent expires).
type frameError struct {
	class     string // invalid_frame | unsupported_protocol
	requestID string // "" when the frame cannot be correlated
	message   string
}

func (e *frameError) Error() string { return e.message }

func badFrame(id, format string, a ...any) *frameError {
	return &frameError{class: "invalid_frame", requestID: id, message: fmt.Sprintf(format, a...)}
}

// errFrameTooLarge marks a frame above the request limit.
var errFrameTooLarge = errors.New("frame too large")

// readFrame reads one newline-terminated frame of at most max bytes
// (excluding the newline). An oversized frame is detected before it is
// buffered in full or decoded.
func readFrame(r *bufio.Reader, max int) ([]byte, error) {
	var buf []byte
	for {
		chunk, err := r.ReadSlice('\n')
		if len(buf)+len(chunk) > max+1 {
			return nil, errFrameTooLarge
		}
		buf = append(buf, chunk...)
		switch {
		case err == nil:
			return buf[:len(buf)-1], nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF):
			if len(buf) == 0 {
				return nil, io.EOF
			}
			return nil, io.ErrUnexpectedEOF
		default:
			return nil, err
		}
	}
}

// request is a decoded request frame.
type request struct {
	id          string
	operation   string
	input       ojson.Value
	hostContext *hostContext
}

// hostContext is broker-owned identity/root data (never model input).
type hostContext struct {
	mode          string
	canonicalRoot string
	sessionID     string
	agent         string
	messageID     string
	callID        string
	runtimeFacts  *ojson.Value
}

// identity is the trusted invocation identity a prepared intent is
// bound to.
func (h *hostContext) identity() string {
	return strings.Join([]string{h.mode, h.canonicalRoot, h.sessionID, h.agent, h.messageID, h.callID}, "\x00")
}

// frame is either a request or a cancel.
type frame struct {
	cancel bool
	req    request
}

func strictKeys(v ojson.Value, where, id string, allowed ...string) *frameError {
	ok := map[string]bool{}
	for _, k := range allowed {
		ok[k] = true
	}
	for _, m := range v.Members() {
		if !ok[m.Key] {
			return badFrame(id, "%s: Unrecognized key: %q", where, m.Key)
		}
	}
	return nil
}

// decodeFrame parses and validates one frame envelope.
func decodeFrame(line []byte) (frame, *frameError) {
	parsed, err := ojson.Parse(line)
	if err != nil {
		return frame{}, badFrame("", "Invalid protocol frame: %v", err)
	}
	if len(parsed.Duplicates) > 0 {
		d := parsed.Duplicates[0]
		p := d.Key
		if d.Path != "" {
			p = d.Path + "." + d.Key
		}
		return frame{}, badFrame("", "Invalid protocol frame: duplicate member name %s", p)
	}
	v := parsed.Value
	if v.Kind() != ojson.Object {
		return frame{}, badFrame("", "Invalid protocol frame: expected an object")
	}
	// Correlate early so errors can carry the request id.
	id := ""
	if rid, ok := v.Get("requestId"); ok && rid.Kind() == ojson.String && requestIDPattern.MatchString(rid.Str()) {
		id = rid.Str()
	}
	typ, _ := v.Get("type")
	if typ.Kind() != ojson.String {
		return frame{}, badFrame(id, "Invalid protocol frame: type must be \"request\" or \"cancel\"")
	}
	pv, ok := v.Get("protocolVersion")
	if !ok || pv.Kind() != ojson.Number {
		return frame{}, badFrame(id, "Invalid protocol frame: protocolVersion is required")
	}
	if pv.NumberLiteral() != "1" {
		return frame{}, &frameError{class: "unsupported_protocol", requestID: id, message: fmt.Sprintf("Unsupported protocolVersion %s; this core speaks protocolVersion 1", pv.NumberLiteral())}
	}
	if id == "" {
		return frame{}, badFrame("", "Invalid protocol frame: requestId must match ^[A-Za-z0-9._:-]{1,128}$")
	}
	switch typ.Str() {
	case "cancel":
		if fe := strictKeys(v, "cancel", id, "type", "protocolVersion", "requestId"); fe != nil {
			return frame{}, fe
		}
		return frame{cancel: true, req: request{id: id}}, nil
	case "request":
	default:
		return frame{}, badFrame(id, "Invalid protocol frame: type must be \"request\" or \"cancel\"")
	}
	if fe := strictKeys(v, "request", id, "type", "protocolVersion", "requestId", "operation", "input", "hostContext"); fe != nil {
		return frame{}, fe
	}
	op, _ := v.Get("operation")
	if op.Kind() != ojson.String || !knownOperation(op.Str()) {
		return frame{}, badFrame(id, "Invalid protocol frame: unknown operation")
	}
	input, ok := v.Get("input")
	if !ok || input.Kind() != ojson.Object {
		return frame{}, badFrame(id, "Invalid protocol frame: input must be an object")
	}
	req := request{id: id, operation: op.Str(), input: input}
	if hc, ok := v.Get("hostContext"); ok {
		h, fe := decodeHostContext(hc, id, req.operation)
		if fe != nil {
			return frame{}, fe
		}
		req.hostContext = h
	}
	if req.operation != "shiori.handshake" && req.hostContext == nil {
		return frame{}, badFrame(id, "Invalid protocol frame: hostContext is required for %s", req.operation)
	}
	return frame{req: req}, nil
}

func decodeHostContext(v ojson.Value, id, op string) (*hostContext, *frameError) {
	if v.Kind() != ojson.Object {
		return nil, badFrame(id, "Invalid protocol frame: hostContext must be an object")
	}
	if fe := strictKeys(v, "hostContext", id, "mode", "canonicalRoot", "sessionID", "agent", "messageID", "callID", "runtimeFacts"); fe != nil {
		return nil, fe
	}
	h := &hostContext{}
	for _, f := range []struct {
		key string
		dst *string
	}{{"mode", &h.mode}, {"canonicalRoot", &h.canonicalRoot}, {"sessionID", &h.sessionID}, {"agent", &h.agent}, {"messageID", &h.messageID}, {"callID", &h.callID}} {
		m, ok := v.Get(f.key)
		if !ok {
			continue
		}
		if m.Kind() != ojson.String {
			return nil, badFrame(id, "Invalid protocol frame: hostContext.%s must be a string", f.key)
		}
		*f.dst = m.Str()
	}
	if h.mode == "" || h.canonicalRoot == "" {
		return nil, badFrame(id, "Invalid protocol frame: hostContext.mode and hostContext.canonicalRoot are required")
	}
	if rf, ok := v.Get("runtimeFacts"); ok {
		if op != "workplan_doctor" {
			return nil, badFrame(id, "Invalid protocol frame: hostContext.runtimeFacts is accepted only for workplan_doctor")
		}
		h.runtimeFacts = &rf
	}
	return h, nil
}
