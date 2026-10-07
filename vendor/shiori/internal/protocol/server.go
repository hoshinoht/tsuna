package protocol

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hoshinoht/shiori/internal/advisor"
	"github.com/hoshinoht/shiori/internal/engine"
	"github.com/hoshinoht/shiori/internal/input"
	"github.com/hoshinoht/shiori/internal/model"
	"github.com/hoshinoht/shiori/internal/ojson"
	"github.com/hoshinoht/shiori/internal/snapshot"
	"github.com/hoshinoht/shiori/internal/storage"
)

// DefaultIdleTimeout is the approved idle exit.
const DefaultIdleTimeout = 10 * time.Minute

// Options configure one stdio connection.
type Options struct {
	In  io.Reader
	Out io.Writer
	Err io.Writer

	CoreVersion      string
	IdleTimeout      time.Duration // 0 = DefaultIdleTimeout; <0 disables
	MaxFrameBytes    int           // 0 = DefaultMaxFrameBytes
	MaxResponseBytes int           // 0 = DefaultMaxResponseBytes

	// Hooks are test-only storage collaborators (fault injection, barriers).
	Hooks storage.Hooks
	// Durability overrides the handshake probe (tests).
	Durability *Durability
	// WriteSupported overrides the platform write gate (tests).
	WriteSupported *bool
	// Compaction sets the compaction advisor thresholds (nil: the
	// defaults); from the trusted serve flag, never from a frame.
	Compaction *advisor.Thresholds
	// JournalVersion is the journal format of writes (0: the default).
	JournalVersion int
	// Rebase is the default of workplan_update's rebase member.
	Rebase bool
}

// ErrIdle is returned by Serve after an idle exit.
var ErrIdle = errors.New("idle timeout")

type server struct {
	opts   Options
	out    io.Writer
	outMu  sync.Mutex
	errw   io.Writer
	ctx    context.Context
	cancel context.CancelFunc

	mu          sync.Mutex
	handshaken  bool
	boundRoot   string
	engine      *engine.Engine
	cache       *snapshot.Cache         // shared by the bound engine
	live        map[string]*liveRequest // requestId -> in-flight request
	intents     map[string]*intent      // intentId -> prepared intent
	byPrepareID map[string]*intent      // prepare requestId -> intent (until commit/discard/expiry)
	secrets     []string                // capabilities issued (redacted from diagnostics)
	lastFrame   time.Time
	wg          sync.WaitGroup
	closed      bool
}

type liveRequest struct {
	cancel    context.CancelFunc
	responded bool
	commit    bool // a commit reports its own (possibly uncertain) outcome
}

type intent struct {
	id         string
	prepareID  string
	digest     string
	capability string
	identity   string
	tool       string
	workplanID string
	prepared   *engine.Prepared
	state      string // prepared | committing
	cancel     context.CancelFunc
}

// Serve runs one connection until EOF, a fatal frame error, or idle exit.
func Serve(parent context.Context, opts Options) error {
	if opts.MaxFrameBytes <= 0 {
		opts.MaxFrameBytes = DefaultMaxFrameBytes
	}
	if opts.MaxResponseBytes <= 0 {
		opts.MaxResponseBytes = DefaultMaxResponseBytes
	}
	if opts.IdleTimeout == 0 {
		opts.IdleTimeout = DefaultIdleTimeout
	}
	if opts.Err == nil {
		opts.Err = io.Discard
	}
	ctx, cancel := context.WithCancel(parent)
	s := &server{
		opts: opts, out: opts.Out, errw: opts.Err, ctx: ctx, cancel: cancel,
		live: map[string]*liveRequest{}, intents: map[string]*intent{}, byPrepareID: map[string]*intent{},
		lastFrame: time.Now(),
	}
	defer s.shutdown()

	frames := make(chan []byte)
	readErr := make(chan error, 1)
	go func() {
		r := bufio.NewReaderSize(opts.In, 64<<10)
		for {
			line, err := readFrame(r, opts.MaxFrameBytes)
			if err != nil {
				readErr <- err
				return
			}
			select {
			case frames <- line:
			case <-ctx.Done():
				return
			}
		}
	}()

	var idle <-chan time.Time
	var ticker *time.Ticker
	if opts.IdleTimeout > 0 {
		tick := opts.IdleTimeout / 4
		if tick > time.Second {
			tick = time.Second
		}
		if tick <= 0 {
			tick = time.Millisecond
		}
		ticker = time.NewTicker(tick)
		defer ticker.Stop()
		idle = ticker.C
	}
	for {
		select {
		case <-parent.Done():
			s.logf("host signal: cancelling in-flight requests")
			return nil
		case err := <-readErr:
			if errors.Is(err, errFrameTooLarge) {
				s.fatal(&frameError{class: "invalid_frame", message: fmt.Sprintf("Protocol frame exceeds the %d-byte request limit", opts.MaxFrameBytes)})
				return err
			}
			if errors.Is(err, io.EOF) {
				s.logf("stdin closed: cancelling in-flight requests; prepared intents expire")
				return nil
			}
			s.logf("transport error: %v", err)
			return err
		case line := <-frames:
			s.mu.Lock()
			s.lastFrame = time.Now()
			s.mu.Unlock()
			if fe := s.handleLine(line); fe != nil {
				s.fatal(fe)
				return fe
			}
		case <-idle:
			s.mu.Lock()
			quiet := len(s.live) == 0 && len(s.intents) == 0 && time.Since(s.lastFrame) >= opts.IdleTimeout
			s.mu.Unlock()
			if quiet {
				s.logf("idle for %s with no live request or prepared intent: exiting", opts.IdleTimeout)
				return ErrIdle
			}
		}
	}
}

// shutdown cancels everything and waits for request goroutines, so every
// in-flight commit reaches a truthful (possibly uncertain) outcome.
func (s *server) shutdown() {
	s.cancel()
	s.mu.Lock()
	for _, in := range s.intents {
		if in.cancel != nil {
			in.cancel()
		}
	}
	s.intents = map[string]*intent{}
	s.byPrepareID = map[string]*intent{}
	s.mu.Unlock()
	s.wg.Wait()
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
}

// fatal reports a frame error and the connection closes.
func (s *server) fatal(fe *frameError) {
	id := fe.requestID
	if id == "" {
		id = "_"
	}
	s.logf("fatal frame error (%s): %s", fe.class, fe.message)
	s.writeError(id, fe.class, fe.message, nil)
}

func (s *server) handleLine(line []byte) *frameError {
	f, fe := decodeFrame(line)
	if fe != nil {
		return fe
	}
	if f.cancel {
		s.handleCancel(f.req.id)
		return nil
	}
	req := f.req
	s.mu.Lock()
	if _, dup := s.live[req.id]; dup {
		s.mu.Unlock()
		return badFrame(req.id, "Invalid protocol frame: requestId %s is already live on this connection", req.id)
	}
	if _, dup := s.byPrepareID[req.id]; dup {
		s.mu.Unlock()
		return badFrame(req.id, "Invalid protocol frame: requestId %s is bound to a live prepared intent", req.id)
	}
	if !s.handshaken && req.operation != "shiori.handshake" {
		s.mu.Unlock()
		return &frameError{class: "unsupported_protocol", requestID: req.id, message: "The first request on a connection must be shiori.handshake"}
	}
	if s.handshaken && req.operation == "shiori.handshake" {
		s.mu.Unlock()
		return badFrame(req.id, "Invalid protocol frame: shiori.handshake was already completed on this connection")
	}
	s.mu.Unlock()

	if req.operation == "shiori.handshake" {
		return s.handshake(req)
	}
	ctx, cancel := context.WithCancel(s.ctx)
	lr := &liveRequest{cancel: cancel, commit: req.operation == "shiori.commit"}
	s.mu.Lock()
	s.live[req.id] = lr
	s.mu.Unlock()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer cancel()
		s.dispatch(ctx, req)
		s.mu.Lock()
		delete(s.live, req.id)
		s.mu.Unlock()
	}()
	return nil
}

// handleCancel cancels a live request, or expires the intent a finished
// prepare request produced. A cancelled intent can never be committed;
// a late approval cannot revive it. Unknown ids are ignored (idempotent).
func (s *server) handleCancel(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if in, ok := s.byPrepareID[id]; ok {
		if in.state == "committing" && in.cancel != nil {
			in.cancel()
		}
		delete(s.intents, in.id)
		delete(s.byPrepareID, id)
		s.logfLocked("request %s cancelled: prepared intent expired", id)
	}
	lr, ok := s.live[id]
	if !ok {
		return
	}
	lr.cancel()
	if !lr.commit && !lr.responded {
		lr.responded = true
		s.writeErrorLocked(id, "cancelled", "The workplan request was cancelled; nothing was changed.", nil)
	}
}

// respond writes a response unless the request was already answered
// (cancelled reads/prepares answer immediately).
func (s *server) respond(id string, build func() ojson.Value) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if lr, ok := s.live[id]; ok {
		if lr.responded {
			return
		}
		lr.responded = true
	}
	s.writeFrameLocked(id, build())
}

func (s *server) writeFrameLocked(id string, v ojson.Value) {
	b := ojson.AppendCompact(nil, v)
	if len(b) > s.opts.MaxResponseBytes {
		b = ojson.AppendCompact(nil, errorFrame(id, "unsupported_capability",
			fmt.Sprintf("Response for request %s exceeds the %d-byte response frame limit; use includeMarkdown=false, workplan_inspect or workplan_resume", id, s.opts.MaxResponseBytes), nil))
	}
	b = append(b, '\n')
	s.outMu.Lock()
	defer s.outMu.Unlock()
	if _, err := s.out.Write(b); err != nil {
		s.logfLocked("stdout write failed: %v", err)
	}
}

func (s *server) writeError(id, class, msg string, extra func(*ojson.Builder)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writeErrorLocked(id, class, msg, extra)
}

func (s *server) writeErrorLocked(id, class, msg string, extra func(*ojson.Builder)) {
	s.writeFrameLocked(id, errorFrame(id, class, msg, extra))
}

func envelope(id string) *ojson.Builder {
	return ojson.NewObject(6).
		Set("type", ojson.StringValue("response")).
		Set("protocolVersion", ojson.IntValue(ProtocolVersion)).
		Set("requestId", ojson.StringValue(id))
}

func errorFrame(id, class, msg string, extra func(*ojson.Builder)) ojson.Value {
	eb := ojson.NewObject(6).Set("class", ojson.StringValue(class)).Set("message", ojson.StringValue(msg))
	if extra != nil {
		extra(eb)
	}
	return envelope(id).Set("ok", ojson.BoolValue(false)).Set("error", eb.Value()).Value()
}

func (s *server) handshake(req request) *frameError {
	if fe := strictKeys(req.input, "input", req.id, "client", "protocolVersions"); fe != nil {
		return fe
	}
	pvs, ok := req.input.Get("protocolVersions")
	if !ok || pvs.Kind() != ojson.Array || len(pvs.Elems()) == 0 {
		return badFrame(req.id, "Invalid protocol frame: protocolVersions must be a non-empty array")
	}
	supported := false
	for _, e := range pvs.Elems() {
		if e.Kind() == ojson.Number && e.NumberLiteral() == "1" {
			supported = true
		}
	}
	if !supported {
		return &frameError{class: "unsupported_protocol", requestID: req.id, message: "No common protocol version; this core speaks protocolVersion 1"}
	}
	d := s.opts.Durability
	if d == nil {
		probe := ProbeDurability()
		d = &probe
	}
	platformOK := PlatformSupported(runtime.GOOS, runtime.GOARCH)
	write := platformOK
	if s.opts.WriteSupported != nil {
		write = *s.opts.WriteSupported
	}
	ops := append(append([]string{}, ToolOperations...), ControlOperations...)
	result := ojson.NewObject(12).
		Set("coreVersion", ojson.StringValue(s.opts.CoreVersion)).
		Set("protocolVersion", ojson.IntValue(ProtocolVersion)).
		Set("contractVersion", ojson.StringValue("v1")).
		Set("operations", ojson.StringsValue(ops)).
		Set("hashAlgorithms", ojson.StringsValue([]string{"workplan-plan-v1", "workplan-state-v1"})).
		Set("platform", ojson.NewObject(3).
			Set("os", ojson.StringValue(runtime.GOOS)).
			Set("arch", ojson.StringValue(runtime.GOARCH)).
			Set("supported", ojson.BoolValue(platformOK)).Value()).
		Set("durability", d.Value()).
		Set("writeSupported", ojson.BoolValue(write)).
		Set("limits", ojson.NewObject(6).
			Set("maxFrameBytes", ojson.IntValue(int64(s.opts.MaxFrameBytes))).
			Set("maxResponseBytes", ojson.IntValue(int64(s.opts.MaxResponseBytes))).
			Set("maxArtifactBytes", ojson.IntValue(int64(snapshot.DefaultLimits.MaxArtifactBytes))).
			Set("maxNesting", ojson.IntValue(ojson.MaxDepth)).
			Set("resumeMaxChars", ojson.NewObject(3).
				Set("min", ojson.IntValue(engine.MinResumeMaxChars)).
				Set("max", ojson.IntValue(engine.MaxResumeMaxChars)).
				Set("default", ojson.IntValue(engine.DefaultResumeMaxChars)).Value()).
			Set("idleTimeoutMs", ojson.IntValue(s.opts.IdleTimeout.Milliseconds())).Value()).
		Set("extensions", ojson.StringsValue([]string{"hostContext.runtimeFacts", "prepared.capability"})).
		Value()
	s.mu.Lock()
	s.handshaken = true
	s.writeFrameLocked(req.id, envelope(req.id).Set("ok", ojson.BoolValue(true)).Set("result", result).Value())
	s.mu.Unlock()
	return nil
}

// PlatformSupported is the approved write/read gate.
func PlatformSupported(goos, goarch string) bool {
	return (goos == "darwin" && goarch == "arm64") || (goos == "linux" && goarch == "amd64")
}

// engineFor binds the connection to exactly one canonical project root.
func (s *server) engineFor(h *hostContext) (*engine.Engine, error) {
	if h.mode != "native" {
		return nil, &classed{class: "unsupported_capability", msg: "shiori serve accepts only hostContext.mode=native; the standalone CLI is the local-operator surface"}
	}
	if !filepath.IsAbs(h.canonicalRoot) || filepath.Clean(h.canonicalRoot) != h.canonicalRoot {
		return nil, &classed{class: "invalid_input", msg: "hostContext.canonicalRoot must be an absolute, clean path"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.engine != nil {
		if h.canonicalRoot != s.boundRoot {
			return nil, &classed{class: "invalid_input", msg: "hostContext.canonicalRoot differs from the project root this connection is bound to"}
		}
		return s.engine, nil
	}
	e, err := engine.New(h.canonicalRoot)
	if err != nil {
		return nil, err
	}
	if e.Root != h.canonicalRoot {
		return nil, &classed{class: "invalid_input", msg: "hostContext.canonicalRoot is not canonical (it resolves through a symbolic link)"}
	}
	e.Compaction = s.opts.Compaction
	e.JournalVersion = s.opts.JournalVersion
	e.Rebase = s.opts.Rebase
	if s.cache == nil {
		s.cache = snapshot.NewCache(snapshot.DefaultCacheBudget)
	}
	e.Cache = s.cache
	s.engine = e
	s.boundRoot = e.Root
	return e, nil
}

// classed is a protocol-level error with an explicit class.
type classed struct {
	class string
	msg   string
}

func (c *classed) Error() string { return c.msg }

func (s *server) dispatch(ctx context.Context, req request) {
	start := time.Now()
	class := "ok"
	defer func() {
		s.logf("request %s %s -> %s (%s)", req.id, req.operation, class, time.Since(start).Round(time.Microsecond))
	}()
	fail := func(err error) {
		class = s.failRequest(req.id, err)
	}
	switch req.operation {
	case "shiori.commit":
		s.commit(ctx, req, fail)
		return
	case "shiori.discard":
		s.discard(req, fail)
		return
	}
	e, err := s.engineFor(req.hostContext)
	if err != nil {
		fail(err)
		return
	}
	tool := strings.TrimPrefix(req.operation, "workplan_")
	switch tool {
	case "list", "read", "inspect", "validate", "resume", "doctor":
		text, value, err := runRead(e, tool, req.input, req.hostContext)
		if err != nil {
			fail(err)
			return
		}
		if ctx.Err() != nil {
			class = "cancelled"
			return
		}
		s.respond(req.id, func() ojson.Value { return resultFrame(req.id, text, value) })
		return
	}
	data, err := input.ParseMutationInput(tool, req.input, input.SurfaceNative)
	if err != nil {
		fail(err)
		return
	}
	prep, err := e.Prepare(tool, data)
	if err != nil {
		fail(err)
		return
	}
	if ctx.Err() != nil {
		class = "cancelled"
		return
	}
	if prep.Intent == nil {
		// Nothing would change (preview, unchanged result): no
		// authorization is requested and no intent is registered.
		out, err := e.Execute(ctx, prep, nil, engine.ExecOptions{})
		if err != nil {
			fail(err)
			return
		}
		s.respond(req.id, func() ojson.Value { return resultFrame(req.id, outputText(out), out.Value) })
		return
	}
	if !s.writeSupported() {
		fail(&classed{class: "unsupported_capability", msg: fmt.Sprintf("Workplan writes are not supported on %s/%s by this core; read operations remain available", runtime.GOOS, runtime.GOARCH)})
		return
	}
	cap := randomHex(32)
	expected := ""
	if h, ok := data.Get("expectedHash"); ok && h.Kind() == ojson.String {
		expected = h.Str()
	}
	in := &intent{
		id:         "intent-" + randomHex(16),
		prepareID:  req.id,
		digest:     prep.Intent.Digest(),
		capability: cap,
		identity:   req.hostContext.identity(),
		tool:       req.operation,
		workplanID: prep.Intent.WorkplanID,
		prepared:   prep,
		state:      "prepared",
	}
	s.mu.Lock()
	if ctx.Err() != nil {
		s.mu.Unlock()
		class = "cancelled"
		return
	}
	s.intents[in.id] = in
	s.byPrepareID[req.id] = in
	s.secrets = append(s.secrets, cap)
	lr := s.live[req.id]
	if lr != nil {
		lr.responded = true
	}
	s.writeFrameLocked(req.id, envelope(req.id).
		Set("ok", ojson.BoolValue(true)).
		Set("prepared", preparedValue(in, prep.Intent, expected)).Value())
	s.mu.Unlock()
	class = "prepared"
}

func (s *server) writeSupported() bool {
	if s.opts.WriteSupported != nil {
		return *s.opts.WriteSupported
	}
	return PlatformSupported(runtime.GOOS, runtime.GOARCH)
}

// capabilityAuthorizer is the definitive host approval already presented
// on this private connection: the adapter's commit carried the intent's
// broker-held capability after the host allowed exactly these resources.
type capabilityAuthorizer struct{ digest string }

func (a capabilityAuthorizer) Authorize(_ context.Context, req engine.AuthRequest) error {
	if req.Digest != a.digest {
		return engine.ErrDenied
	}
	return nil
}

func (s *server) commit(ctx context.Context, req request, fail func(error)) {
	if fe := strictKeys(req.input, "input", req.id, "intentId", "intentDigest", "capability"); fe != nil {
		fail(&classed{class: "invalid_input", msg: fe.message})
		return
	}
	get := func(k string) string {
		v, _ := req.input.Get(k)
		if v.Kind() != ojson.String {
			return ""
		}
		return v.Str()
	}
	intentID, digest, capability := get("intentId"), get("intentDigest"), get("capability")
	s.mu.Lock()
	in, ok := s.intents[intentID]
	if !ok || in.state != "prepared" {
		s.mu.Unlock()
		fail(&classed{class: "stale_state", msg: "Prepared workplan intent is not live on this connection (cancelled, expired, already used or never prepared); prepare the mutation again"})
		return
	}
	// A mismatched commit attempt burns the intent (fail closed).
	mismatch := ""
	switch {
	case subtle.ConstantTimeCompare([]byte(digest), []byte(in.digest)) != 1:
		mismatch = "intent digest"
	case capability == "" || subtle.ConstantTimeCompare([]byte(capability), []byte(in.capability)) != 1:
		mismatch = "capability"
	case req.hostContext.identity() != in.identity:
		mismatch = "invocation identity"
	}
	if mismatch != "" {
		delete(s.intents, in.id)
		delete(s.byPrepareID, in.prepareID)
		s.mu.Unlock()
		fail(&classed{class: "permission_rejected", msg: "Workplan commit does not match the prepared intent's " + mismatch + "; the intent was discarded and nothing was changed"})
		return
	}
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	in.state = "committing"
	in.cancel = cancel
	s.mu.Unlock()

	e := s.engine
	out, err := e.Execute(cctx, in.prepared, capabilityAuthorizer{digest: in.digest}, engine.ExecOptions{Hooks: s.opts.Hooks})
	s.mu.Lock()
	delete(s.intents, in.id)
	delete(s.byPrepareID, in.prepareID)
	s.mu.Unlock()
	if err != nil {
		fail(err)
		return
	}
	s.respond(req.id, func() ojson.Value { return resultFrame(req.id, outputText(out), out.Value) })
}

func (s *server) discard(req request, fail func(error)) {
	if fe := strictKeys(req.input, "input", req.id, "intentId", "intentDigest"); fe != nil {
		fail(&classed{class: "invalid_input", msg: fe.message})
		return
	}
	idv, _ := req.input.Get("intentId")
	s.mu.Lock()
	discarded := false
	if in, ok := s.intents[idv.Str()]; ok && in.state == "prepared" && in.identity == req.hostContext.identity() {
		delete(s.intents, in.id)
		delete(s.byPrepareID, in.prepareID)
		discarded = true
	}
	s.mu.Unlock()
	s.respond(req.id, func() ojson.Value {
		return envelope(req.id).Set("ok", ojson.BoolValue(true)).
			Set("result", ojson.NewObject(1).Set("discarded", ojson.BoolValue(discarded)).Value()).Value()
	})
}

// failRequest maps an error to a structured protocol error and answers.
func (s *server) failRequest(id string, err error) string {
	class := classOf(err)
	s.mu.Lock()
	defer s.mu.Unlock()
	if lr, ok := s.live[id]; ok {
		if lr.responded {
			return class
		}
		lr.responded = true
	}
	s.writeErrorLocked(id, class, err.Error(), func(b *ojson.Builder) {
		var ie *input.InputError
		var gate interface{ StructuredIssues() []model.Issue }
		var list []model.Issue
		if errors.As(err, &ie) {
			list = ie.Issues
		} else if errors.As(err, &gate) {
			list = gate.StructuredIssues() // plan / step status gate field paths
		}
		if len(list) > 0 {
			issues := make([]ojson.Value, len(list))
			for i, is := range list {
				p := is.PathString()
				if p == "" {
					p = "$"
				}
				issues[i] = ojson.NewObject(2).Set("path", ojson.StringValue(p)).Set("message", ojson.StringValue(is.Message)).Value()
			}
			b.Set("issues", ojson.ArrayValue(issues))
		}
		var stale *engine.StaleHashError
		if errors.As(err, &stale) {
			b.Set("currentStateHash", ojson.StringValue(stale.Current))
		}
		var rec *storage.RecoveryRequiredError
		if errors.As(err, &rec) && s.engine != nil {
			if r, rerr := filepath.Rel(s.engine.Root, rec.JournalPath); rerr == nil {
				b.Set("recoveryJournal", ojson.StringValue(filepath.ToSlash(r)))
			}
			id := strings.TrimSuffix(filepath.Base(rec.JournalPath), ".transaction.json")
			b.Set("retrieval", ojson.StringValue("workplan_doctor id="+id))
		}
	})
	return class
}

func classOf(err error) string {
	var c *classed
	if errors.As(err, &c) {
		return c.class
	}
	var rec *storage.RecoveryRequiredError
	if errors.As(err, &rec) && rec.Uncertain {
		return "outcome_uncertain"
	}
	return engine.ErrorClass(err)
}

func outputText(o engine.Output) string {
	if o.Text != "" {
		// Patch: the native tool result is {output, metadata}.
		return string(ojson.Pretty(ojson.NewObject(2).Set("output", ojson.StringValue(o.Text)).Set("metadata", o.Metadata).Value()))
	}
	return o.String()
}

// resultFrame carries the exact tool result text (never re-serialized by
// the adapter) and the current hashes when the result has them.
func resultFrame(id, text string, v ojson.Value) ojson.Value {
	b := envelope(id).Set("ok", ojson.BoolValue(true)).
		Set("result", ojson.NewObject(1).Set("text", ojson.StringValue(text)).Value())
	if h := hashesOf(v); h != nil {
		b.Set("hashes", *h)
	}
	return b.Value()
}

func hashesOf(v ojson.Value) *ojson.Value {
	if v.Kind() != ojson.Object {
		return nil
	}
	hb := ojson.NewObject(2)
	n := 0
	for _, k := range []string{"planHash", "stateHash"} {
		if h, ok := v.Get(k); ok && h.Kind() == ojson.String && len(h.Str()) == 64 {
			hb.Set(k, h)
			n++
		}
	}
	if n == 0 {
		return nil
	}
	out := hb.Value()
	return &out
}

func preparedValue(in *intent, si *storage.Intent, expectedHash string) ojson.Value {
	r := si.Resources()
	sorted := func(ss []string) ojson.Value {
		c := append([]string{}, ss...)
		sort.Strings(c)
		return ojson.StringsValue(c)
	}
	write := append([]string{}, r.Write...)
	if si.Recovery == "" {
		write = append(write, r.Journal) // the journal is published then removed
	}
	del := append([]string{}, r.Delete...)
	if si.Recovery != "" {
		del = append(del, r.Journal) // recovery removes the existing journal
	}
	targets := make([]ojson.Value, len(si.Targets))
	for i, t := range si.Targets {
		targets[i] = ojson.NewObject(4).
			Set("path", ojson.StringValue(filepath.Join(si.Root, filepath.FromSlash(t.Rel)))).
			Set("kind", ojson.StringValue(t.Kind)).
			Set("beforeHash", nullable(t.BeforeHash())).
			Set("afterHash", nullable(t.AfterHash())).Value()
	}
	// null only for a fresh create (must-be-absent preconditions); the
	// storage engine rechecks every read precondition under the locks.
	expected := ojson.NullValue()
	if expectedHash != "" {
		expected = ojson.StringValue(expectedHash)
	}
	return ojson.NewObject(10).
		Set("intentId", ojson.StringValue(in.id)).
		Set("intentDigest", ojson.StringValue(in.digest)).
		Set("capability", ojson.StringValue(in.capability)).
		Set("operation", ojson.StringValue(si.Operation)).
		Set("tool", ojson.StringValue(in.tool)).
		Set("workplanId", ojson.StringValue(si.WorkplanID)).
		Set("canonicalRoot", ojson.StringValue(si.Root)).
		Set("expectedStateHash", expected).
		Set("resources", ojson.NewObject(6).
			Set("readPaths", sorted(r.Read)).
			Set("writePaths", sorted(write)).
			Set("deletePaths", sorted(del)).
			Set("lockPaths", sorted(r.Lock)).
			Set("stagingPaths", sorted(r.Staging)).
			Set("archivePaths", sorted(r.Archive)).Value()).
		Set("targets", ojson.ArrayValue(targets)).Value()
}

func nullable(s string) ojson.Value {
	if s == "" {
		return ojson.NullValue()
	}
	return ojson.StringValue(s)
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// runRead validates native input and runs a read-only operation.
func runRead(e *engine.Engine, tool string, raw ojson.Value, h *hostContext) (string, ojson.Value, error) {
	return e.RunRead(tool, raw, h.runtimeFacts)
}
