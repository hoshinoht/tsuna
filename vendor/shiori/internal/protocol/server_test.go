package protocol

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hoshinoht/shiori/internal/advisor"
	"github.com/hoshinoht/shiori/internal/engine"
	"github.com/hoshinoht/shiori/internal/input"
	"github.com/hoshinoht/shiori/internal/ojson"
	"github.com/hoshinoht/shiori/internal/storage"
)

// ---- harness ----------------------------------------------------------

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuf) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

type harness struct {
	t      *testing.T
	inW    *io.PipeWriter
	lines  chan []byte
	stderr *syncBuf
	done   chan error
	cancel context.CancelFunc
}

func start(t *testing.T, opts Options) *harness {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	h := &harness{t: t, inW: inW, lines: make(chan []byte, 64), stderr: &syncBuf{}, done: make(chan error, 1)}
	opts.In, opts.Out, opts.Err = inR, outW, h.stderr
	if opts.CoreVersion == "" {
		opts.CoreVersion = "test"
	}
	if opts.IdleTimeout == 0 {
		opts.IdleTimeout = -1
	}
	if opts.Durability == nil {
		opts.Durability = &Durability{AtomicRename: "supported", FileSync: "supported", DirectorySync: "supported", ObservedAt: "test"}
	}
	if opts.WriteSupported == nil {
		yes := true
		opts.WriteSupported = &yes
	}
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	go func() {
		err := Serve(ctx, opts)
		outW.Close()
		inR.Close()
		h.done <- err
	}()
	go func() {
		r := bufio.NewReaderSize(outR, 1<<20)
		for {
			line, err := r.ReadBytes('\n')
			if len(line) > 0 {
				h.lines <- line
			}
			if err != nil {
				close(h.lines)
				return
			}
		}
	}()
	t.Cleanup(func() {
		cancel()
		inW.Close()
		select {
		case <-h.done:
		case <-time.After(5 * time.Second):
		}
	})
	return h
}

func (h *harness) send(s string) {
	h.t.Helper()
	if _, err := io.WriteString(h.inW, s+"\n"); err != nil {
		h.t.Fatalf("send: %v", err)
	}
}

func (h *harness) sendJSON(v any) {
	h.t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		h.t.Fatal(err)
	}
	h.send(string(b))
}

type resp map[string]any

func (h *harness) recv() resp {
	h.t.Helper()
	select {
	case line, ok := <-h.lines:
		if !ok {
			h.t.Fatalf("connection closed; stderr:\n%s", h.stderr.String())
		}
		var r resp
		if err := json.Unmarshal(line, &r); err != nil {
			h.t.Fatalf("bad response %q: %v", line, err)
		}
		return r
	case <-time.After(10 * time.Second):
		h.t.Fatalf("timed out waiting for a response; stderr:\n%s", h.stderr.String())
	}
	return nil
}

func (h *harness) noResponse(d time.Duration) {
	h.t.Helper()
	select {
	case line, ok := <-h.lines:
		if ok {
			h.t.Fatalf("unexpected response %s", line)
		}
	case <-time.After(d):
	}
}

func (h *harness) waitExit() error {
	h.t.Helper()
	select {
	case err := <-h.done:
		h.done <- err
		return err
	case <-time.After(10 * time.Second):
		h.t.Fatalf("server did not exit; stderr:\n%s", h.stderr.String())
	}
	return nil
}

func (r resp) ok() bool            { v, _ := r["ok"].(bool); return v }
func (r resp) id() string          { v, _ := r["requestId"].(string); return v }
func (r resp) obj(k string) resp   { v, _ := r[k].(map[string]any); return v }
func (r resp) str(k string) string { v, _ := r[k].(string); return v }
func (r resp) errClass() string    { return r.obj("error").str("class") }
func (r resp) text() string        { return r.obj("result").str("text") }

func (h *harness) handshake() resp {
	h.t.Helper()
	h.sendJSON(map[string]any{"type": "request", "protocolVersion": 1, "requestId": "hs", "operation": "shiori.handshake", "input": map[string]any{"client": "test", "protocolVersions": []int{1}}})
	r := h.recv()
	if !r.ok() {
		h.t.Fatalf("handshake failed: %v", r)
	}
	return r
}

func host(root string) map[string]any {
	return map[string]any{"mode": "native", "canonicalRoot": root, "sessionID": "ses_1", "agent": "orchestrator", "messageID": "msg_1", "callID": "call_1"}
}

func (h *harness) call(id, op, root string, input map[string]any) resp {
	h.t.Helper()
	h.sendJSON(map[string]any{"type": "request", "protocolVersion": 1, "requestId": id, "operation": op, "input": input, "hostContext": host(root)})
	r := h.recv()
	if r.id() != id {
		h.t.Fatalf("response for %q, want %q: %v", r.id(), id, r)
	}
	return r
}

func (h *harness) cancelReq(id string) {
	h.sendJSON(map[string]any{"type": "cancel", "protocolVersion": 1, "requestId": id})
}

func commitInput(p resp) map[string]any {
	return map[string]any{"intentId": p.str("intentId"), "intentDigest": p.str("intentDigest"), "capability": p.str("capability")}
}

// ---- fixtures ----------------------------------------------------------

func fixtureRoot(t *testing.T, name string) string {
	t.Helper()
	src := filepath.Join("..", "..", "testdata", "fixtures", name)
	dst := t.TempDir()
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o600)
	})
	if err != nil {
		t.Fatal(err)
	}
	real, err := filepath.EvalSymlinks(dst)
	if err != nil {
		t.Fatal(err)
	}
	return real
}

type fileState struct {
	data  string
	mode  fs.FileMode
	mtime time.Time
}

func fingerprint(t *testing.T, root string) map[string]fileState {
	t.Helper()
	m := map[string]fileState{}
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		st, _ := os.Lstat(p)
		fsx := fileState{mode: st.Mode(), mtime: st.ModTime()}
		if !d.IsDir() {
			b, _ := os.ReadFile(p)
			fsx.data = string(b)
		}
		m[p] = fsx
		return nil
	})
	return m
}

func sameFingerprint(t *testing.T, a, b map[string]fileState) {
	t.Helper()
	if len(a) != len(b) {
		t.Fatalf("file set changed: %d -> %d entries", len(a), len(b))
	}
	for p, x := range a {
		y, ok := b[p]
		// Directory mtimes may move when a lock file is created and
		// removed; every file's bytes, mode and mtime must not.
		if !ok || x.data != y.data || x.mode != y.mode || (!x.mode.IsDir() && !x.mtime.Equal(y.mtime)) {
			t.Fatalf("root changed at %s", p)
		}
	}
}

func readHash(t *testing.T, h *harness, root, id string) string {
	t.Helper()
	r := h.call("rh-"+fmt.Sprint(time.Now().UnixNano()), "workplan_read", root, map[string]any{"id": id, "includeMarkdown": false})
	if !r.ok() {
		t.Fatalf("read failed: %v", r)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(r.text()), &out); err != nil {
		t.Fatal(err)
	}
	return out["stateHash"].(string)
}

// ---- handshake and framing ---------------------------------------------

func TestHandshakeAdvertisesContract(t *testing.T) {
	h := start(t, Options{})
	r := h.handshake().obj("result")
	if r["protocolVersion"].(float64) != 1 || r.str("contractVersion") != "v1" {
		t.Fatalf("versions: %v", r)
	}
	ops := r["operations"].([]any)
	if len(ops) != 16 {
		t.Fatalf("operations: %v", ops)
	}
	d := r.obj("durability")
	for _, k := range []string{"atomicRename", "fileSync", "directorySync"} {
		if d.str(k) == "" {
			t.Fatalf("durability %s missing", k)
		}
	}
	lim := r.obj("limits")
	if lim["maxFrameBytes"].(float64) != DefaultMaxFrameBytes || lim["maxNesting"].(float64) != 128 {
		t.Fatalf("limits: %v", lim)
	}
	if _, ok := r["writeSupported"].(bool); !ok {
		t.Fatal("writeSupported missing")
	}
}

func TestRealDurabilityProbeReportsObservedFacts(t *testing.T) {
	d := ProbeDurability()
	for _, v := range []string{d.AtomicRename, d.FileSync, d.DirectorySync} {
		if v != "supported" && v != "unsupported" && v != "unknown" {
			t.Fatalf("bad durability value %q", v)
		}
	}
}

func TestRequestBeforeHandshakeFailsClosed(t *testing.T) {
	h := start(t, Options{})
	h.sendJSON(map[string]any{"type": "request", "protocolVersion": 1, "requestId": "r1", "operation": "workplan_list", "input": map[string]any{}, "hostContext": host("/")})
	r := h.recv()
	if r.errClass() != "unsupported_protocol" || r.id() != "r1" {
		t.Fatalf("got %v", r)
	}
	if err := h.waitExit(); err == nil {
		t.Fatal("connection stayed open")
	}
}

func TestHandshakeVersionMismatch(t *testing.T) {
	h := start(t, Options{})
	h.sendJSON(map[string]any{"type": "request", "protocolVersion": 1, "requestId": "hs", "operation": "shiori.handshake", "input": map[string]any{"protocolVersions": []int{2}}})
	if r := h.recv(); r.errClass() != "unsupported_protocol" {
		t.Fatalf("got %v", r)
	}
	h.waitExit()

	h2 := start(t, Options{})
	h2.send(`{"type":"request","protocolVersion":2,"requestId":"hs","operation":"shiori.handshake","input":{"protocolVersions":[2]}}`)
	if r := h2.recv(); r.errClass() != "unsupported_protocol" || r.id() != "hs" {
		t.Fatalf("got %v", r)
	}
	h2.waitExit()
}

func TestFrameRejections(t *testing.T) {
	deep := strings.Repeat("[", 200) + strings.Repeat("]", 200)
	cases := []struct {
		name, frame, class, id string
	}{
		{"malformed", `{"type":"request",`, "invalid_frame", "_"},
		{"not-object", `[1,2]`, "invalid_frame", "_"},
		{"duplicate-envelope-key", `{"type":"request","type":"request","protocolVersion":1,"requestId":"d1","operation":"shiori.handshake","input":{"protocolVersions":[1]}}`, "invalid_frame", "_"},
		{"duplicate-input-key", `{"type":"request","protocolVersion":1,"requestId":"d2","operation":"shiori.handshake","input":{"protocolVersions":[1],"protocolVersions":[1]}}`, "invalid_frame", "_"},
		{"unknown-envelope-key", `{"type":"request","protocolVersion":1,"requestId":"u1","operation":"shiori.handshake","input":{"protocolVersions":[1]},"approved":true}`, "invalid_frame", "u1"},
		{"unknown-handshake-key", `{"type":"request","protocolVersion":1,"requestId":"u2","operation":"shiori.handshake","input":{"protocolVersions":[1],"approved":true}}`, "invalid_frame", "u2"},
		{"unknown-operation", `{"type":"request","protocolVersion":1,"requestId":"u3","operation":"sh -c true","input":{}}`, "invalid_frame", "u3"},
		{"bad-request-id", `{"type":"request","protocolVersion":1,"requestId":"a b","operation":"shiori.handshake","input":{"protocolVersions":[1]}}`, "invalid_frame", "_"},
		{"too-deep", `{"type":"request","protocolVersion":1,"requestId":"n1","operation":"shiori.handshake","input":{"x":` + deep + `}}`, "invalid_frame", "_"},
		{"string-version", `{"type":"request","protocolVersion":"1","requestId":"v1","operation":"shiori.handshake","input":{"protocolVersions":[1]}}`, "invalid_frame", "v1"},
		{"float-version", `{"type":"request","protocolVersion":1.0,"requestId":"v2","operation":"shiori.handshake","input":{"protocolVersions":[1]}}`, "unsupported_protocol", "v2"},
		{"bad-type", `{"type":"response","protocolVersion":1,"requestId":"t1","ok":true}`, "invalid_frame", "t1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := start(t, Options{})
			h.send(c.frame)
			r := h.recv()
			if r.errClass() != c.class || r.id() != c.id || r.ok() {
				t.Fatalf("got %v", r)
			}
			if err := h.waitExit(); err == nil {
				t.Fatal("connection stayed open after an invalid frame")
			}
		})
	}
}

func TestFrameSizeLimitRejectsBeforeDecoding(t *testing.T) {
	h := start(t, Options{MaxFrameBytes: 4096})
	h.handshake()
	big := `{"type":"request","protocolVersion":1,"requestId":"big","operation":"workplan_list","input":{"x":"` + strings.Repeat("a", 8192) + `"}}`
	h.send(big)
	r := h.recv()
	if r.errClass() != "invalid_frame" || r.id() != "_" || !strings.Contains(r.obj("error").str("message"), "4096-byte") {
		t.Fatalf("got %v", r)
	}
	if err := h.waitExit(); err == nil {
		t.Fatal("connection stayed open")
	}
}

func TestResponseFrameLimit(t *testing.T) {
	root := fixtureRoot(t, "full-valid")
	h := start(t, Options{MaxResponseBytes: 1500})
	h.handshake()
	r := h.call("r1", "workplan_read", root, map[string]any{"id": "full-plan"})
	if r.errClass() != "unsupported_capability" {
		t.Fatalf("got %v", r)
	}
}

func TestUnknownInputFieldRejectsButConnectionContinues(t *testing.T) {
	root := fixtureRoot(t, "minimal-valid")
	h := start(t, Options{})
	h.handshake()
	for _, bad := range []map[string]any{
		{"id": "minimal", "workspaceRoot": "/tmp"},
		{"id": "minimal", "sessionID": "forged"},
		{"id": "minimal", "approved": true},
	} {
		r := h.call("bad", "workplan_read", root, bad)
		if r.errClass() != "invalid_input" || !strings.Contains(r.obj("error").str("message"), "Unrecognized key") {
			t.Fatalf("got %v", r)
		}
	}
	// Nested unknown keys reject too, with a path.
	r := h.call("nested", "workplan_create", root, map[string]any{"id": "x", "goal": "g", "phases": []any{map[string]any{"title": "p", "extra": 1}}})
	if r.errClass() != "invalid_input" || !strings.Contains(r.obj("error").str("message"), `phases.0: Unrecognized key: "extra"`) {
		t.Fatalf("got %v", r)
	}
	if ok := h.call("fine", "workplan_list", root, map[string]any{}); !ok.ok() {
		t.Fatalf("connection unusable after an input error: %v", ok)
	}
}

func TestHostContextRules(t *testing.T) {
	root := fixtureRoot(t, "minimal-valid")
	t.Run("missing", func(t *testing.T) {
		h := start(t, Options{})
		h.handshake()
		h.send(`{"type":"request","protocolVersion":1,"requestId":"m","operation":"workplan_list","input":{}}`)
		if r := h.recv(); r.errClass() != "invalid_frame" {
			t.Fatalf("got %v", r)
		}
	})
	t.Run("unknown-key", func(t *testing.T) {
		h := start(t, Options{})
		h.handshake()
		hc := host(root)
		hc["approved"] = true
		h.sendJSON(map[string]any{"type": "request", "protocolVersion": 1, "requestId": "m", "operation": "workplan_list", "input": map[string]any{}, "hostContext": hc})
		if r := h.recv(); r.errClass() != "invalid_frame" {
			t.Fatalf("got %v", r)
		}
	})
	t.Run("runtime-facts-only-doctor", func(t *testing.T) {
		h := start(t, Options{})
		h.handshake()
		hc := host(root)
		hc["runtimeFacts"] = map[string]any{}
		h.sendJSON(map[string]any{"type": "request", "protocolVersion": 1, "requestId": "m", "operation": "workplan_list", "input": map[string]any{}, "hostContext": hc})
		if r := h.recv(); r.errClass() != "invalid_frame" {
			t.Fatalf("got %v", r)
		}
	})
	t.Run("cli-mode", func(t *testing.T) {
		h := start(t, Options{})
		h.handshake()
		hc := host(root)
		hc["mode"] = "cli"
		h.sendJSON(map[string]any{"type": "request", "protocolVersion": 1, "requestId": "m", "operation": "workplan_list", "input": map[string]any{}, "hostContext": hc})
		if r := h.recv(); r.errClass() != "unsupported_capability" {
			t.Fatalf("got %v", r)
		}
	})
	t.Run("non-canonical-and-rebinding", func(t *testing.T) {
		link := filepath.Join(t.TempDir(), "link")
		if err := os.Symlink(root, link); err != nil {
			t.Fatal(err)
		}
		h := start(t, Options{})
		h.handshake()
		if r := h.call("a", "workplan_list", link, map[string]any{}); r.errClass() != "invalid_input" {
			t.Fatalf("symlinked root accepted: %v", r)
		}
		if r := h.call("b", "workplan_list", root, map[string]any{}); !r.ok() {
			t.Fatalf("got %v", r)
		}
		other := fixtureRoot(t, "full-valid")
		if r := h.call("c", "workplan_list", other, map[string]any{}); r.errClass() != "invalid_input" {
			t.Fatalf("connection rebound to another root: %v", r)
		}
	})
}

func TestDuplicateLiveRequestID(t *testing.T) {
	root := fixtureRoot(t, "minimal-valid")
	h := start(t, Options{})
	h.handshake()
	hash := readHash(t, h, root, "minimal")
	p := h.call("prep", "workplan_update", root, map[string]any{"id": "minimal", "expectedHash": hash, "title": "T"})
	if p.obj("prepared") == nil {
		t.Fatalf("got %v", p)
	}
	// "prep" stays bound to the prepared intent: reusing it is refused.
	h.sendJSON(map[string]any{"type": "request", "protocolVersion": 1, "requestId": "prep", "operation": "workplan_list", "input": map[string]any{}, "hostContext": host(root)})
	if r := h.recv(); r.errClass() != "invalid_frame" {
		t.Fatalf("got %v", r)
	}
}

// ---- read operations ------------------------------------------------------

func TestReadOperationsReturnExactToolText(t *testing.T) {
	root := fixtureRoot(t, "full-valid")
	e, err := engine.New(root)
	if err != nil {
		t.Fatal(err)
	}
	h := start(t, Options{})
	h.handshake()
	before := fingerprint(t, root)
	ids := listIDs(t, e)
	id := ids[0]
	cases := []struct {
		op    string
		input map[string]any
	}{
		{"workplan_list", map[string]any{}},
		{"workplan_read", map[string]any{"id": id}},
		{"workplan_inspect", map[string]any{"id": id, "limit": 2}},
		{"workplan_validate", map[string]any{"id": id}},
		{"workplan_resume", map[string]any{"id": id, "maxChars": 4096}},
		{"workplan_doctor", map[string]any{"limit": 5}},
		{"workplan_compact_preview", map[string]any{"id": id, "archiveReason": "tidy", "noteIndexes": []int{}}},
	}
	for i, c := range cases {
		r := h.call(fmt.Sprintf("r%d", i), c.op, root, c.input)
		if !r.ok() {
			t.Fatalf("%s: %v", c.op, r)
		}
		want := directText(t, e, c.op, c.input)
		if r.text() != want {
			t.Fatalf("%s: protocol text differs from the engine's tool text", c.op)
		}
	}
	sameFingerprint(t, before, fingerprint(t, root))
}

func listIDs(t *testing.T, e *engine.Engine) []string {
	v, err := e.List(input.ListInput{})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	wps, _ := v.Get("workplans")
	for _, w := range wps.Elems() {
		if valid, _ := w.Get("valid"); valid.Bool() {
			id, _ := w.Get("id")
			ids = append(ids, id.Str())
		}
	}
	if len(ids) == 0 {
		t.Fatal("fixture has no valid plan")
	}
	return ids
}

func directText(t *testing.T, e *engine.Engine, op string, in map[string]any) string {
	t.Helper()
	b, _ := json.Marshal(in)
	p, err := ojson.Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	if op == "workplan_compact_preview" {
		data, err := input.ParseMutationInput("compact_preview", p.Value, input.SurfaceNative)
		if err != nil {
			t.Fatal(err)
		}
		prep, err := e.Prepare("compact_preview", data)
		if err != nil {
			t.Fatal(err)
		}
		out, err := e.Execute(context.Background(), prep, nil, engine.ExecOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	text, _, err := runRead(e, strings.TrimPrefix(op, "workplan_"), p.Value, &hostContext{})
	if err != nil {
		t.Fatal(err)
	}
	return text
}

func TestDoctorRuntimeFactsAreSanitized(t *testing.T) {
	root := fixtureRoot(t, "minimal-valid")
	h := start(t, Options{})
	h.handshake()
	hc := host(root)
	hc["runtimeFacts"] = map[string]any{
		"registrations": map[string]any{"effective": []any{"workplan_read", 7, strings.Repeat("x", 400)}, "configured": "nope"},
		"plugin":        map[string]any{"id": "workplan-tools", "configured": true, "effective": "yes", "canonicalLocation": root},
		"permission":    map[string]any{"status": "known", "agent": "plan", "rules": []any{map[string]any{"resource": "/a", "decision": "maybe", "source": "agent:edit"}, "bad"}, "detail": "d"},
		"builtinPlan":   []any{},
	}
	h.sendJSON(map[string]any{"type": "request", "protocolVersion": 1, "requestId": "d", "operation": "workplan_doctor", "input": map[string]any{}, "hostContext": hc})
	r := h.recv()
	if !r.ok() {
		t.Fatalf("got %v", r)
	}
	var out map[string]any
	json.Unmarshal([]byte(r.text()), &out)
	rf := resp(out["runtimeFacts"].(map[string]any))
	eff := rf.obj("registrations")["effective"].([]any)
	if len(eff) != 2 || len(eff[1].(string)) != 300 || rf.obj("registrations")["configured"] != nil {
		t.Fatalf("registrations: %v", rf.obj("registrations"))
	}
	if rf.obj("plugin")["effective"] != nil || rf.obj("plugin").str("id") != "workplan-tools" {
		t.Fatalf("plugin: %v", rf.obj("plugin"))
	}
	perm := rf.obj("permission")
	rules := perm["rules"].([]any)
	if perm.str("status") != "known" || len(rules) != 1 || rules[0].(map[string]any)["decision"] != "unknown" || perm["sessionID"] != nil {
		t.Fatalf("permission: %v", perm)
	}
	if rf.obj("builtinPlan")["configured"] != nil {
		t.Fatalf("builtinPlan: %v", rf.obj("builtinPlan"))
	}
	if _, ok := rf["host"]; ok {
		t.Fatalf("host facts rendered without being supplied: %v", rf["host"])
	}

	// Host facts are rendered only when supplied, type-checked and
	// bounded.
	hc["runtimeFacts"] = map[string]any{"host": map[string]any{
		"opencodeVersion": strings.Repeat("9", 100), "verified": "no", "verifiedVersions": []any{"2.0.19", 3, "2.0.20"},
		"writes": "maybe", "detail": "Shiori adapter not verified",
	}}
	h.sendJSON(map[string]any{"type": "request", "protocolVersion": 1, "requestId": "d2", "operation": "workplan_doctor", "input": map[string]any{}, "hostContext": hc})
	r = h.recv()
	if !r.ok() {
		t.Fatalf("got %v", r)
	}
	out = nil
	json.Unmarshal([]byte(r.text()), &out)
	hf := resp(out["runtimeFacts"].(map[string]any)).obj("host")
	vv, _ := hf["verifiedVersions"].([]any)
	if len(hf.str("opencodeVersion")) != 60 || hf["verified"] != nil || len(vv) != 2 || hf["writes"] != nil || hf.str("detail") != "Shiori adapter not verified" {
		t.Fatalf("host: %v", hf)
	}
}

// ---- prepare -> authorize -> commit -----------------------------------------

func TestPrepareTouchesNothingAndCommitWritesExactIntent(t *testing.T) {
	root := fixtureRoot(t, "minimal-valid")
	h := start(t, Options{})
	h.handshake()
	hash := readHash(t, h, root, "minimal")
	before := fingerprint(t, root)
	r := h.call("p1", "workplan_update", root, map[string]any{"id": "minimal", "expectedHash": hash, "appendNotes": []string{"from protocol"}})
	p := r.obj("prepared")
	if p == nil {
		t.Fatalf("got %v", r)
	}
	sameFingerprint(t, before, fingerprint(t, root))
	if p.str("expectedStateHash") != hash || p.str("workplanId") != "minimal" || p.str("canonicalRoot") != root || len(p.str("capability")) != 64 {
		t.Fatalf("prepared: %v", p)
	}
	res := p.obj("resources")
	for _, k := range []string{"readPaths", "writePaths", "deletePaths", "lockPaths", "stagingPaths", "archivePaths"} {
		if _, ok := res[k].([]any); !ok {
			t.Fatalf("resources.%s missing: %v", k, res)
		}
		for _, x := range res[k].([]any) {
			if !strings.HasPrefix(x.(string), root+string(os.PathSeparator)) {
				t.Fatalf("resource outside root: %s", x)
			}
		}
	}
	c := h.call("c1", "shiori.commit", root, commitInput(p))
	if !c.ok() {
		t.Fatalf("commit failed: %v", c)
	}
	var out map[string]any
	json.Unmarshal([]byte(c.text()), &out)
	if out["stateHash"] == hash || c.obj("hashes").str("stateHash") != out["stateHash"] {
		t.Fatalf("commit result: %v", out)
	}
	data, _ := os.ReadFile(filepath.Join(root, ".opencode/workplan/minimal.json"))
	if !strings.Contains(string(data), "from protocol") {
		t.Fatal("commit did not publish the after image")
	}
	if _, err := os.Stat(filepath.Join(root, ".opencode/workplan/minimal.transaction.json")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("journal left behind")
	}
	// Single use: replaying the same commit is refused.
	if again := h.call("c2", "shiori.commit", root, commitInput(p)); again.errClass() != "stale_state" {
		t.Fatalf("replay accepted: %v", again)
	}
	if !strings.Contains(h.stderr.String(), "request c1 shiori.commit -> ok") {
		t.Fatalf("diagnostics missing:\n%s", h.stderr.String())
	}
	if strings.Contains(h.stderr.String(), p.str("capability")) {
		t.Fatal("capability leaked to stderr")
	}
}

func TestCommitBindingRejectionsBurnTheIntent(t *testing.T) {
	for _, tc := range []string{"digest", "capability", "no-capability", "identity"} {
		t.Run(tc, func(t *testing.T) {
			root := fixtureRoot(t, "minimal-valid")
			h := start(t, Options{})
			h.handshake()
			hash := readHash(t, h, root, "minimal")
			p := h.call("p", "workplan_update", root, map[string]any{"id": "minimal", "expectedHash": hash, "title": "X"}).obj("prepared")
			before := fingerprint(t, root)
			in := commitInput(p)
			hc := host(root)
			switch tc {
			case "digest":
				in["intentDigest"] = strings.Repeat("0", 64)
			case "capability":
				in["capability"] = strings.Repeat("a", 64)
			case "no-capability":
				delete(in, "capability")
			case "identity":
				hc["callID"] = "call_other"
			}
			h.sendJSON(map[string]any{"type": "request", "protocolVersion": 1, "requestId": "c", "operation": "shiori.commit", "input": in, "hostContext": hc})
			if r := h.recv(); r.errClass() != "permission_rejected" {
				t.Fatalf("got %v", r)
			}
			// The intent is burned: even the genuine commit is refused.
			if r := h.call("c2", "shiori.commit", root, commitInput(p)); r.errClass() != "stale_state" {
				t.Fatalf("burned intent committed: %v", r)
			}
			sameFingerprint(t, before, fingerprint(t, root))
		})
	}
}

func TestCommitRejectsInjectedFields(t *testing.T) {
	root := fixtureRoot(t, "minimal-valid")
	h := start(t, Options{})
	h.handshake()
	hash := readHash(t, h, root, "minimal")
	p := h.call("p", "workplan_update", root, map[string]any{"id": "minimal", "expectedHash": hash, "title": "X"}).obj("prepared")
	in := commitInput(p)
	in["approved"] = true
	if r := h.call("c", "shiori.commit", root, in); r.errClass() != "invalid_input" {
		t.Fatalf("got %v", r)
	}
}

func TestCancelBeforeAuthorizationExpiresIntent(t *testing.T) {
	root := fixtureRoot(t, "minimal-valid")
	h := start(t, Options{})
	h.handshake()
	hash := readHash(t, h, root, "minimal")
	before := fingerprint(t, root)
	p := h.call("p", "workplan_update", root, map[string]any{"id": "minimal", "expectedHash": hash, "title": "late"}).obj("prepared")
	h.cancelReq("p")
	// A late approval after cancellation cannot reactivate the request.
	if r := h.call("c", "shiori.commit", root, commitInput(p)); r.errClass() != "stale_state" {
		t.Fatalf("got %v", r)
	}
	sameFingerprint(t, before, fingerprint(t, root))
}

func TestDiscardReleasesIntent(t *testing.T) {
	root := fixtureRoot(t, "minimal-valid")
	h := start(t, Options{})
	h.handshake()
	hash := readHash(t, h, root, "minimal")
	p := h.call("p", "workplan_update", root, map[string]any{"id": "minimal", "expectedHash": hash, "title": "d"}).obj("prepared")
	r := h.call("d", "shiori.discard", root, map[string]any{"intentId": p.str("intentId"), "intentDigest": p.str("intentDigest")})
	if !r.ok() || r.obj("result")["discarded"] != true {
		t.Fatalf("got %v", r)
	}
	if r := h.call("c", "shiori.commit", root, commitInput(p)); r.errClass() != "stale_state" {
		t.Fatalf("got %v", r)
	}
}

func TestStateChangeAfterPreparationIsRejectedUnderLock(t *testing.T) {
	root := fixtureRoot(t, "minimal-valid")
	h := start(t, Options{})
	h.handshake()
	hash := readHash(t, h, root, "minimal")
	p := h.call("p", "workplan_update", root, map[string]any{"id": "minimal", "expectedHash": hash, "title": "one"}).obj("prepared")
	md := filepath.Join(root, ".opencode/workplan/minimal.md")
	b, _ := os.ReadFile(md)
	os.WriteFile(md, append(b, []byte("\nexternal edit\n")...), 0o600)
	after := fingerprint(t, root)
	r := h.call("c", "shiori.commit", root, commitInput(p))
	if r.ok() || (r.errClass() != "stale_state" && r.errClass() != "external_edit_conflict") {
		t.Fatalf("got %v", r)
	}
	sameFingerprint(t, after, fingerprint(t, root))
}

func TestCancelDuringCommitBeforeJournal(t *testing.T) {
	root := fixtureRoot(t, "minimal-valid")
	reached := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	h := start(t, Options{Hooks: storage.Hooks{AfterLock: func() {
		once.Do(func() { close(reached); <-release })
	}}})
	h.handshake()
	hash := readHash(t, h, root, "minimal")
	before := fingerprint(t, root)
	p := h.call("p", "workplan_update", root, map[string]any{"id": "minimal", "expectedHash": hash, "title": "cancel"}).obj("prepared")
	h.sendJSON(map[string]any{"type": "request", "protocolVersion": 1, "requestId": "c", "operation": "shiori.commit", "input": commitInput(p), "hostContext": host(root)})
	<-reached
	h.cancelReq("c")
	time.Sleep(20 * time.Millisecond)
	close(release)
	r := h.recv()
	if r.id() != "c" || r.errClass() != "cancelled" {
		t.Fatalf("got %v", r)
	}
	after := fingerprint(t, root)
	// Directories may pre-exist; nothing else may change.
	sameFingerprint(t, before, after)
}

func TestCancelDuringCommitAfterJournalIsUncertain(t *testing.T) {
	root := fixtureRoot(t, "minimal-valid")
	reached := make(chan struct{})
	release := make(chan struct{})
	h := start(t, Options{Hooks: storage.Hooks{Fault: func(point string) error {
		if point == storage.FaultJournalSync {
			close(reached)
			<-release
		}
		return nil
	}}})
	h.handshake()
	hash := readHash(t, h, root, "minimal")
	p := h.call("p", "workplan_update", root, map[string]any{"id": "minimal", "expectedHash": hash, "title": "uncertain"}).obj("prepared")
	h.sendJSON(map[string]any{"type": "request", "protocolVersion": 1, "requestId": "c", "operation": "shiori.commit", "input": commitInput(p), "hostContext": host(root)})
	<-reached
	h.cancelReq("p") // cancelling the original invocation also cancels its commit
	time.Sleep(20 * time.Millisecond)
	close(release)
	r := h.recv()
	e := r.obj("error")
	if r.errClass() != "outcome_uncertain" || e.str("recoveryJournal") != ".opencode/workplan/minimal.transaction.json" || e.str("retrieval") != "workplan_doctor id=minimal" {
		t.Fatalf("got %v", r)
	}
	if _, err := os.Stat(filepath.Join(root, ".opencode/workplan/minimal.transaction.json")); err != nil {
		t.Fatal("durable journal evidence was not preserved")
	}
}

func TestCancelInFlightReadAnswersImmediately(t *testing.T) {
	root := fixtureRoot(t, "minimal-valid")
	h := start(t, Options{})
	h.handshake()
	h.sendJSON(map[string]any{"type": "request", "protocolVersion": 1, "requestId": "r", "operation": "workplan_list", "input": map[string]any{}, "hostContext": host(root)})
	h.cancelReq("r")
	r := h.recv()
	if r.id() != "r" {
		t.Fatalf("got %v", r)
	}
	// Either the read won the race or the cancel did; never both.
	h.noResponse(100 * time.Millisecond)
	// Cancelling an unknown or finished request is a no-op.
	h.cancelReq("zzz")
	if r := h.call("after", "workplan_list", root, map[string]any{}); !r.ok() {
		t.Fatalf("got %v", r)
	}
}

func TestDisconnectExpiresIntentsAndReconnectCannotReplay(t *testing.T) {
	root := fixtureRoot(t, "minimal-valid")
	h := start(t, Options{})
	h.handshake()
	hash := readHash(t, h, root, "minimal")
	before := fingerprint(t, root)
	p := h.call("p", "workplan_update", root, map[string]any{"id": "minimal", "expectedHash": hash, "title": "disconnected"}).obj("prepared")
	h.inW.Close()
	if err := h.waitExit(); err != nil {
		t.Fatalf("EOF exit: %v", err)
	}
	h2 := start(t, Options{})
	// A new connection needs a new handshake before anything else.
	h2.sendJSON(map[string]any{"type": "request", "protocolVersion": 1, "requestId": "c", "operation": "shiori.commit", "input": commitInput(p), "hostContext": host(root)})
	if r := h2.recv(); r.errClass() != "unsupported_protocol" {
		t.Fatalf("got %v", r)
	}
	h3 := start(t, Options{})
	h3.handshake()
	if r := h3.call("c", "shiori.commit", root, commitInput(p)); r.errClass() != "stale_state" {
		t.Fatalf("replayed across reconnect: %v", r)
	}
	sameFingerprint(t, before, fingerprint(t, root))
}

func TestUnchangedMutationNeedsNoIntent(t *testing.T) {
	root := fixtureRoot(t, "minimal-valid")
	h := start(t, Options{})
	h.handshake()
	hash := readHash(t, h, root, "minimal")
	// Resetting generated Markdown to itself prepares nothing.
	r := h.call("m", "workplan_reset", root, map[string]any{"id": "minimal", "expectedHash": hash, "mode": "markdown-only"})
	if !r.ok() || r.obj("prepared") != nil || r.text() == "" {
		t.Fatalf("got %v", r)
	}
}

// The wipe preview is a result without an
// intent; the apply is a prepared intent whose resources list the archive
// and the sidecar deletions, and commit performs exactly that.
func TestResetWipePreviewThenPreparedApply(t *testing.T) {
	root := fixtureRoot(t, "full-valid")
	h := start(t, Options{})
	h.handshake()
	hash := readHash(t, h, root, "full-plan")
	before := fingerprint(t, root)
	r := h.call("pv", "workplan_reset", root, map[string]any{"id": "full-plan", "expectedHash": hash, "mode": "wipe"})
	if !r.ok() || r.obj("prepared") != nil {
		t.Fatalf("preview: %v", r)
	}
	var pv map[string]any
	json.Unmarshal([]byte(r.text()), &pv)
	tok, _ := pv["previewToken"].(string)
	sameFingerprint(t, before, fingerprint(t, root))
	r = h.call("ap", "workplan_reset", root, map[string]any{"id": "full-plan", "expectedHash": hash, "mode": "wipe", "previewToken": tok, "confirmation": "WIPE_PLAN_CONTENT"})
	p := r.obj("prepared")
	if p == nil {
		t.Fatalf("apply: %v", r)
	}
	res := p.obj("resources")
	arch, _ := res["archivePaths"].([]any)
	dels, _ := res["deletePaths"].([]any)
	if len(arch) != 1 || len(dels) != 2 || !strings.HasSuffix(dels[0].(string), "full-plan.checkpoint.json") || !strings.HasSuffix(dels[1].(string), "full-plan.dependencies.json") {
		t.Fatalf("resources: %v", res)
	}
	sameFingerprint(t, before, fingerprint(t, root))
	if c := h.call("c", "shiori.commit", root, commitInput(p)); !c.ok() {
		t.Fatalf("commit: %v", c)
	}
	for _, n := range []string{"full-plan.checkpoint.json", "full-plan.dependencies.json"} {
		if _, err := os.Stat(filepath.Join(root, ".opencode/workplan", n)); !os.IsNotExist(err) {
			t.Fatalf("%s not removed", n)
		}
	}
	if _, err := os.Stat(arch[0].(string)); err != nil {
		t.Fatalf("archive: %v", err)
	}
}

func TestWritesRefusedWhenPlatformUnsupported(t *testing.T) {
	root := fixtureRoot(t, "minimal-valid")
	no := false
	h := start(t, Options{WriteSupported: &no})
	r := h.handshake().obj("result")
	if r["writeSupported"] != false {
		t.Fatalf("got %v", r)
	}
	hash := readHash(t, h, root, "minimal")
	if r := h.call("p", "workplan_update", root, map[string]any{"id": "minimal", "expectedHash": hash, "title": "x"}); r.errClass() != "unsupported_capability" {
		t.Fatalf("got %v", r)
	}
}

func TestErrorsCarryStructuredFields(t *testing.T) {
	root := fixtureRoot(t, "minimal-valid")
	h := start(t, Options{})
	h.handshake()
	hash := readHash(t, h, root, "minimal")
	r := h.call("s", "workplan_update", root, map[string]any{"id": "minimal", "expectedHash": strings.Repeat("0", 64), "title": "x"})
	if r.errClass() != "stale_state" || r.obj("error").str("currentStateHash") != hash {
		t.Fatalf("got %v", r)
	}
	r = h.call("m", "workplan_read", root, map[string]any{"id": "absent"})
	if r.errClass() != "missing_artifact" {
		t.Fatalf("got %v", r)
	}
	r = h.call("i", "workplan_update", root, map[string]any{"id": "minimal", "title": "x"})
	if r.errClass() != "invalid_input" || len(r.obj("error")["issues"].([]any)) == 0 {
		t.Fatalf("got %v", r)
	}
}

// On the native path the status gate is an
// invalid_structure error with field-path issues and prepares nothing;
// patch validate returns the issue list after commit.
func TestStatusGateAndPatchValidateIssues(t *testing.T) {
	root := fixtureRoot(t, "draft-empty")
	h := start(t, Options{})
	h.handshake()
	hash := readHash(t, h, root, "draft-plan")
	before := fingerprint(t, root)
	r := h.call("g", "workplan_update", root, map[string]any{"id": "draft-plan", "expectedHash": hash, "status": "in_progress"})
	if r.errClass() != "invalid_structure" || r.obj("prepared") != nil {
		t.Fatalf("gate: %v", r)
	}
	var paths []string
	for _, is := range r.obj("error")["issues"].([]any) {
		paths = append(paths, is.(map[string]any)["path"].(string))
	}
	if strings.Join(paths, ",") != "goal,phases" {
		t.Fatalf("issue paths %v", paths)
	}
	sameFingerprint(t, before, fingerprint(t, root))
	patch := "*** Begin Patch\n*** Update File: .opencode/workplan/draft-plan.md\n@@\n-# draft-plan\n+# draft-plan (edited)\n*** End Patch"
	r = h.call("p", "workplan_patch", root, map[string]any{"id": "draft-plan", "expectedHash": hash, "patchText": patch, "validate": true})
	p := r.obj("prepared")
	if p == nil {
		t.Fatalf("patch: %v", r)
	}
	c := h.call("c", "shiori.commit", root, commitInput(p))
	var out struct {
		Metadata struct {
			Validation struct {
				IssueCount int      `json:"issueCount"`
				Issues     []string `json:"issues"`
				Warnings   []string `json:"warnings"`
			} `json:"validation"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal([]byte(c.text()), &out); err != nil || !c.ok() {
		t.Fatalf("commit: %v %v", c, err)
	}
	v := out.Metadata.Validation
	if v.IssueCount == 0 || len(v.Issues) != v.IssueCount || !strings.HasPrefix(v.Issues[0], "goal: ") || len(v.Warnings) != 1 {
		t.Fatalf("validation %+v", v)
	}
}

func TestConcurrentRequestsAreMultiplexed(t *testing.T) {
	root := fixtureRoot(t, "full-valid")
	h := start(t, Options{})
	h.handshake()
	for i := 0; i < 20; i++ {
		h.sendJSON(map[string]any{"type": "request", "protocolVersion": 1, "requestId": fmt.Sprintf("q%d", i), "operation": "workplan_list", "input": map[string]any{}, "hostContext": host(root)})
	}
	seen := map[string]bool{}
	for i := 0; i < 20; i++ {
		r := h.recv()
		if !r.ok() || seen[r.id()] {
			t.Fatalf("got %v", r)
		}
		seen[r.id()] = true
	}
}

// ---- lifecycle ------------------------------------------------------------

func TestIdleExit(t *testing.T) {
	h := start(t, Options{IdleTimeout: 60 * time.Millisecond})
	h.handshake()
	if err := h.waitExit(); !errors.Is(err, ErrIdle) {
		t.Fatalf("got %v", err)
	}
	if !strings.Contains(h.stderr.String(), "idle") {
		t.Fatal("idle exit not diagnosed")
	}
}

func TestIdleExitWaitsForPreparedIntents(t *testing.T) {
	root := fixtureRoot(t, "minimal-valid")
	h := start(t, Options{IdleTimeout: 80 * time.Millisecond})
	h.handshake()
	hash := readHash(t, h, root, "minimal")
	p := h.call("p", "workplan_update", root, map[string]any{"id": "minimal", "expectedHash": hash, "title": "held"}).obj("prepared")
	select {
	case err := <-h.done:
		t.Fatalf("exited while an intent awaited authorization: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	h.call("d", "shiori.discard", root, map[string]any{"intentId": p.str("intentId"), "intentDigest": p.str("intentDigest")})
	if err := h.waitExit(); !errors.Is(err, ErrIdle) {
		t.Fatalf("got %v", err)
	}
}

func TestHostSignalStopsServe(t *testing.T) {
	h := start(t, Options{})
	h.handshake()
	h.cancel()
	if err := h.waitExit(); err != nil {
		t.Fatalf("got %v", err)
	}
}

func TestRedact(t *testing.T) {
	s := Redact("Authorization: Basic b3BlbmNvZGU6c2VjcmV0 token=abc123 cap=deadbeef password: hunter2", []string{"deadbeef"})
	for _, leak := range []string{"b3BlbmNvZGU6c2VjcmV0", "abc123", "deadbeef", "hunter2"} {
		if strings.Contains(s, leak) {
			t.Fatalf("leaked %q in %q", leak, s)
		}
	}
}

// The step status gate carries its field paths too, before any intent is
// prepared.
func TestStepStatusGateIssues(t *testing.T) {
	root := fixtureRoot(t, "minimal-valid")
	h := start(t, Options{})
	h.handshake()
	hash := readHash(t, h, root, "minimal")
	before := fingerprint(t, root)
	r := h.call("g", "workplan_update", root, map[string]any{"id": "minimal", "expectedHash": hash,
		"addSteps": []any{map[string]any{"phaseId": "phase-one", "step": map[string]any{"id": "bare", "title": "Bare", "status": "in_progress", "action": "a"}}}})
	if r.errClass() != "invalid_structure" || r.obj("prepared") != nil {
		t.Fatalf("step gate: %v", r)
	}
	var paths []string
	for _, is := range r.obj("error")["issues"].([]any) {
		paths = append(paths, is.(map[string]any)["path"].(string))
	}
	if strings.Join(paths, ",") != "phases.0.steps.1.validation" {
		t.Fatalf("step gate issue paths %v", paths)
	}
	sameFingerprint(t, before, fingerprint(t, root))
}

// On the native path compact_preview with
// noteRollover is read-only, the prepared apply commits it, and the serve
// option sets the advisor thresholds of the connection's engine.
func TestNoteRolloverPreviewThenPreparedApply(t *testing.T) {
	root := fixtureRoot(t, "large-paging")
	h := start(t, Options{Compaction: &advisor.Thresholds{MinSavingsBytes: 1024, Notes: 10}})
	h.handshake()
	d := h.call("d", "workplan_doctor", root, map[string]any{"id": "big-plan"})
	if !d.ok() || !strings.Contains(d.text(), `"compactionRecommended"`) {
		t.Fatalf("doctor with lowered thresholds: %v", d)
	}
	hash := readHash(t, h, root, "big-plan")
	before := fingerprint(t, root)
	roll := map[string]any{"keepLatest": 50}
	r := h.call("pv", "workplan_compact_preview", root, map[string]any{"id": "big-plan", "archiveReason": "roll", "noteRollover": roll})
	if !r.ok() || r.obj("prepared") != nil {
		t.Fatalf("preview: %v", r)
	}
	var pv map[string]any
	json.Unmarshal([]byte(r.text()), &pv)
	tok, _ := pv["previewToken"].(string)
	sameFingerprint(t, before, fingerprint(t, root))
	if bad := h.call("b", "workplan_compact_preview", root, map[string]any{"id": "big-plan", "archiveReason": "roll", "noteRollover": roll, "noteIndexes": []int{1}}); bad.errClass() != "invalid_input" {
		t.Fatalf("rollover with noteIndexes: %v", bad)
	}
	a := h.call("ap", "workplan_compact", root, map[string]any{"id": "big-plan", "archiveReason": "roll", "noteRollover": roll, "mode": "apply", "previewToken": tok, "confirmation": "ARCHIVE_SELECTED_HISTORY", "expectedHash": hash})
	p := a.obj("prepared")
	if p == nil {
		t.Fatalf("apply: %v", a)
	}
	sameFingerprint(t, before, fingerprint(t, root))
	if c := h.call("c", "shiori.commit", root, commitInput(p)); !c.ok() || !strings.Contains(c.text(), `"savings"`) {
		t.Fatalf("commit: %v", c)
	}
}

// On the native path resume names the withheld
// fields of a stale checkpoint, a rebuild that copies the withheld null is
// refused before anything is prepared, merge=true keeps every field and
// appends a validation line, and a rebuild that drops entries warns.
func TestCheckpointMergeAndWithheldGuards(t *testing.T) {
	root := fixtureRoot(t, "full-valid")
	if err := os.WriteFile(filepath.Join(root, ".opencode/workplan/full-plan.md"), []byte("# edited by hand\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := start(t, Options{})
	h.handshake()
	r := h.call("r", "workplan_resume", root, map[string]any{"id": "full-plan"})
	var packet struct {
		Checkpoint struct {
			Withheld []string `json:"withheld"`
			Summary  *string  `json:"summary"`
		} `json:"checkpoint"`
		Instruction string `json:"instruction"`
	}
	if err := json.Unmarshal([]byte(r.text()), &packet); err != nil || !r.ok() {
		t.Fatalf("resume: %v %v", r, err)
	}
	if strings.Join(packet.Checkpoint.Withheld, ",") != "summary,nextAction,guardrails,references,recentValidation" || packet.Checkpoint.Summary != nil ||
		!strings.Contains(packet.Instruction, ".opencode/workplan/full-plan.checkpoint.json") {
		t.Fatalf("packet %+v", packet)
	}
	hash := readHash(t, h, root, "full-plan")
	before := fingerprint(t, root)
	r = h.call("n", "workplan_checkpoint", root, map[string]any{"id": "full-plan", "expectedHash": hash, "summary": "null DEPLOYED", "nextAction": "x"})
	if r.errClass() != "invalid_input" || r.obj("prepared") != nil {
		t.Fatalf("null rebuild: %v", r)
	}
	issues := r.obj("error")["issues"].([]any)
	if len(issues) != 1 || issues[0].(map[string]any)["path"] != "summary" {
		t.Fatalf("issues %v", issues)
	}
	sameFingerprint(t, before, fingerprint(t, root))
	// Merge refresh.
	r = h.call("m", "workplan_checkpoint", root, map[string]any{"id": "full-plan", "expectedHash": hash, "merge": true, "appendValidation": "deploy ok"})
	p := r.obj("prepared")
	if p == nil {
		t.Fatalf("merge: %v", r)
	}
	c := h.call("mc", "shiori.commit", root, commitInput(p))
	var out struct {
		Checkpoint struct {
			Summary          string   `json:"summary"`
			Guardrails       []string `json:"guardrails"`
			References       []string `json:"references"`
			RecentValidation []string `json:"recentValidation"`
		} `json:"checkpoint"`
		Warnings []string `json:"warnings"`
	}
	if err := json.Unmarshal([]byte(c.text()), &out); err != nil || !c.ok() {
		t.Fatalf("commit: %v %v", c, err)
	}
	if out.Checkpoint.Summary != "Phase A done; B in progress" || len(out.Checkpoint.Guardrails) != 1 || len(out.Checkpoint.References) != 1 ||
		strings.Join(out.Checkpoint.RecentValidation, "|") != "bun test: 3 passed|deploy ok" || out.Warnings != nil {
		t.Fatalf("merged %+v", out)
	}
	// A replacing write that drops entries warns.
	hash = readHash(t, h, root, "full-plan")
	r = h.call("w", "workplan_checkpoint", root, map[string]any{"id": "full-plan", "expectedHash": hash, "summary": "s", "nextAction": "n"})
	c = h.call("wc", "shiori.commit", root, commitInput(r.obj("prepared")))
	out.Warnings = nil
	if err := json.Unmarshal([]byte(c.text()), &out); err != nil || !c.ok() || len(out.Warnings) != 4 ||
		out.Warnings[0] != "guardrails: 1 → 0 (1 previous entry not kept; merge=true keeps omitted fields)" {
		t.Fatalf("warnings %v (%v)", out.Warnings, c)
	}
}

func TestNativeEvidenceIsRecordedAsAgent(t *testing.T) {
	root := fixtureRoot(t, "full-valid")
	h := start(t, Options{})
	h.handshake()
	id := "full-plan"
	hash := readHash(t, h, root, id)
	plan, _ := os.ReadFile(filepath.Join(root, ".opencode/workplan", id+".json"))
	var doc struct {
		Phases []struct {
			ID    string `json:"id"`
			Steps []struct {
				ID string `json:"id"`
			} `json:"steps"`
		} `json:"phases"`
	}
	json.Unmarshal(plan, &doc)
	rec := map[string]any{"phaseId": doc.Phases[0].ID, "stepId": doc.Phases[0].Steps[0].ID, "command": "make test", "exitCode": 0, "output": "ok"}
	p := h.call("p", "workplan_update", root, map[string]any{"id": id, "expectedHash": hash, "recordEvidence": []any{rec}}).obj("prepared")
	if p == nil {
		t.Fatal("not prepared")
	}
	c := h.call("c", "shiori.commit", root, commitInput(p))
	if !c.ok() || c.obj("hashes").str("stateHash") != hash {
		t.Fatalf("commit: %v", c)
	}
	data, err := os.ReadFile(filepath.Join(root, ".opencode/workplan", id+".evidence.json"))
	if err != nil || !strings.Contains(string(data), `"source": "agent"`) {
		t.Fatalf("ledger %s %v", data, err)
	}
}
