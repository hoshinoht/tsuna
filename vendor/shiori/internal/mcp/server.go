// Package mcp serves the workplan tools over the Model Context Protocol
// (stdio transport, JSON-RPC 2.0, revisions 2025-03-26 to 2025-11-25), so
// any MCP client can use them without the OpenCode adapter.
//
// Trust boundaries are the core's: the project root comes from the server
// configuration (--root) or the client's roots, never from tool input;
// writes are prepared without side effects and committed only after an
// approval (MCP elicitation of the exact intent, or the client's own
// tool-call approval when the operator chose that); every precondition is
// rechecked under the locks.
package mcp

import (
	"bufio"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hoshinoht/shiori/internal/advisor"
	"github.com/hoshinoht/shiori/internal/engine"
	"github.com/hoshinoht/shiori/internal/history"
	"github.com/hoshinoht/shiori/internal/input"
	"github.com/hoshinoht/shiori/internal/ojson"
	"github.com/hoshinoht/shiori/internal/protocol"
	"github.com/hoshinoht/shiori/internal/snapshot"
)

// Registration is the model-facing tool surface: the same names,
// descriptions and input schemas the OpenCode adapter registers.
//
//go:embed registration.json
var Registration []byte

// Supported protocol revisions, newest first.
var Versions = []string{"2025-11-25", "2025-06-18", "2025-03-26"}

// Write approval modes.
const (
	ApproveAuto        = "auto"        // elicitation when the client supports it, else client
	ApproveElicitation = "elicitation" // always ask the user through the client; refuse without it
	ApproveClient      = "client"      // the client's tool-call approval authorizes the call
	ApproveDeny        = "deny"        // read-only server
)

// Options configure a server. All of them are trusted operator settings.
type Options struct {
	In             io.Reader
	Out, Err       io.Writer
	Root           string // project root; "" = the client's single root
	ToolPrefix     string // prepended to tool names ("" suits server name "workplan")
	WriteApproval  string
	Version        string
	Compaction     *advisor.Thresholds
	JournalVersion int
	// Rebase is the default of workplan_update's rebase member.
	Rebase bool
	// PollInterval is how often subscribed resources are checked
	// (0: DefaultPollInterval).
	PollInterval time.Duration
}

type tool struct {
	name        string // workplan_* identity
	description string
	input       json.RawMessage
}

var readTools = map[string]bool{"list": true, "read": true, "inspect": true, "validate": true, "resume": true, "doctor": true, "compact_preview": true}

// destructive tools can remove or replace stored content.
var destructive = map[string]bool{"create": true, "update": true, "reset": true, "compact": true}

func loadTools() ([]tool, error) {
	var reg struct {
		Tools []struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			Input       json.RawMessage `json:"input"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(Registration, &reg); err != nil {
		return nil, err
	}
	out := make([]tool, len(reg.Tools))
	for i, t := range reg.Tools {
		out[i] = tool{name: t.Name, description: t.Description, input: t.Input}
	}
	return out, nil
}

type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// JSON-RPC error codes.
const (
	codeParse          = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	// codeResourceNotFound is MCP's resource-not-found error.
	codeResourceNotFound = -32002
	codeInternal         = -32603
)

type server struct {
	opts  Options
	tools []tool
	byMCP map[string]tool

	outMu sync.Mutex
	out   *bufio.Writer

	mu          sync.Mutex
	version     string
	client      string
	elicitation bool
	roots       bool
	rootsStale  bool
	engine      *engine.Engine
	nextID      int64
	pending     map[string]chan message
	inflight    map[string]context.CancelFunc
	wg          sync.WaitGroup

	// Resource subscriptions (resources.go).
	ctx         context.Context
	subs        map[string]string // uri -> plan id
	tokens      map[string]string // uri -> last change token
	listed      bool
	listedPlans string
	watching    bool
	checkMu     sync.Mutex
}

// Serve runs one stdio session until the input closes or ctx ends.
func Serve(ctx context.Context, opts Options) error {
	if opts.WriteApproval == "" {
		opts.WriteApproval = ApproveAuto
	}
	switch opts.WriteApproval {
	case ApproveAuto, ApproveElicitation, ApproveClient, ApproveDeny:
	default:
		return fmt.Errorf("unknown write approval mode %q", opts.WriteApproval)
	}
	tools, err := loadTools()
	if err != nil {
		return err
	}
	s := &server{opts: opts, tools: tools, byMCP: map[string]tool{}, out: bufio.NewWriter(opts.Out),
		pending: map[string]chan message{}, inflight: map[string]context.CancelFunc{},
		subs: map[string]string{}, tokens: map[string]string{}}
	for _, t := range tools {
		s.byMCP[opts.ToolPrefix+strings.TrimPrefix(t.name, "workplan_")] = t
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s.ctx = ctx
	sc := bufio.NewScanner(opts.In)
	sc.Buffer(make([]byte, 64<<10), protocol.DefaultMaxFrameBytes)
	for sc.Scan() {
		line := sc.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var m message
		if err := json.Unmarshal(line, &m); err != nil {
			s.reply(nil, nil, &rpcError{codeParse, "Parse error: " + err.Error()})
			continue
		}
		s.handle(ctx, m)
	}
	cancel()
	s.mu.Lock()
	for _, c := range s.pending {
		close(c)
	}
	s.pending = map[string]chan message{}
	s.mu.Unlock()
	s.wg.Wait()
	return sc.Err()
}

func (s *server) logf(format string, a ...any) {
	if s.opts.Err != nil {
		fmt.Fprintf(s.opts.Err, "shiori mcp: "+format+"\n", a...)
	}
}

func (s *server) write(v any) {
	data, err := json.Marshal(v)
	if err != nil {
		s.logf("encode: %v", err)
		return
	}
	s.outMu.Lock()
	defer s.outMu.Unlock()
	s.out.Write(data)
	s.out.WriteByte('\n')
	s.out.Flush()
}

func (s *server) reply(id json.RawMessage, result any, e *rpcError) {
	if id == nil {
		id = json.RawMessage("null")
	}
	m := map[string]any{"jsonrpc": "2.0", "id": id}
	if e != nil {
		m["error"] = e
	} else {
		m["result"] = result
	}
	s.write(m)
}

func (s *server) handle(ctx context.Context, m message) {
	switch {
	case m.Method == "" && m.ID != nil:
		// A response to one of our requests.
		s.mu.Lock()
		c := s.pending[string(m.ID)]
		delete(s.pending, string(m.ID))
		s.mu.Unlock()
		if c != nil {
			c <- m
		}
	case m.ID == nil:
		s.notification(m)
	case m.Method == "tools/call" || strings.HasPrefix(m.Method, "resources/") && m.Method != "resources/templates/list":
		// May wait on the client (roots, elicitation): run concurrently.
		cctx, cancel := context.WithCancel(ctx)
		s.mu.Lock()
		s.inflight[string(m.ID)] = cancel
		s.mu.Unlock()
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer func() {
				s.mu.Lock()
				delete(s.inflight, string(m.ID))
				s.mu.Unlock()
				cancel()
			}()
			var res any
			var e *rpcError
			switch m.Method {
			case "tools/call":
				res, e = s.callTool(cctx, m.Params)
			case "resources/list":
				res, e = s.listResources(cctx)
			case "resources/read":
				res, e = s.readResource(cctx, m.Params)
			case "resources/subscribe", "resources/unsubscribe":
				if _, err := s.engineFor(cctx); err != nil {
					res, e = nil, &rpcError{codeInternal, err.Error()}
				} else {
					res, e = s.subscribe(m.Params, m.Method == "resources/subscribe")
				}
			default:
				res, e = nil, &rpcError{codeMethodNotFound, "Method not found: " + m.Method}
			}
			if cctx.Err() != nil {
				return // cancelled by the client or the session ended: no response
			}
			s.reply(m.ID, res, e)
		}()
	default:
		res, e := s.request(m)
		s.reply(m.ID, res, e)
	}
}

func (s *server) notification(m message) {
	switch m.Method {
	case "notifications/cancelled":
		var p struct {
			RequestID json.RawMessage `json:"requestId"`
		}
		if json.Unmarshal(m.Params, &p) == nil {
			s.mu.Lock()
			if c := s.inflight[string(p.RequestID)]; c != nil {
				c()
			}
			s.mu.Unlock()
		}
	case "notifications/roots/list_changed":
		s.mu.Lock()
		s.rootsStale = true
		s.mu.Unlock()
	}
}

func (s *server) request(m message) (any, *rpcError) {
	switch m.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
			Capabilities    struct {
				Roots       json.RawMessage `json:"roots"`
				Elicitation json.RawMessage `json:"elicitation"`
			} `json:"capabilities"`
			ClientInfo struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"clientInfo"`
		}
		if err := json.Unmarshal(m.Params, &p); err != nil {
			return nil, &rpcError{codeInvalidParams, "initialize: " + err.Error()}
		}
		version := Versions[0]
		for _, v := range Versions {
			if v == p.ProtocolVersion {
				version = v
			}
		}
		s.mu.Lock()
		s.version = version
		s.client = strings.TrimSpace(p.ClientInfo.Name + " " + p.ClientInfo.Version)
		s.roots = present(p.Capabilities.Roots)
		s.elicitation = elicitForm(p.Capabilities.Elicitation)
		s.mu.Unlock()
		s.logf("initialized %s (protocol %s, roots=%v, elicitation=%v, writes=%s)", s.client, version, s.roots, s.elicitation, s.approvalMode())
		return map[string]any{
			"protocolVersion": version,
			"capabilities": map[string]any{
				"tools":     map[string]any{"listChanged": false},
				"resources": map[string]any{"subscribe": true, "listChanged": true},
			},
			"serverInfo":   map[string]any{"name": "shiori", "version": s.opts.Version},
			"instructions": instructions,
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "resources/templates/list":
		return listTemplates(), nil
	case "tools/list":
		list := make([]any, 0, len(s.tools))
		for _, t := range s.tools {
			short := strings.TrimPrefix(t.name, "workplan_")
			ann := map[string]any{"readOnlyHint": readTools[short], "openWorldHint": false}
			if !readTools[short] {
				ann["destructiveHint"] = destructive[short]
				ann["idempotentHint"] = false
			}
			list = append(list, map[string]any{
				"name":        s.opts.ToolPrefix + short,
				"description": t.description,
				"inputSchema": t.input,
				"annotations": ann,
			})
		}
		return map[string]any{"tools": list}, nil
	}
	return nil, &rpcError{codeMethodNotFound, "Method not found: " + m.Method}
}

const instructions = "Workplan tools keep a durable plan under .opencode/workplan. Start a session with resume (bounded) " +
	"or inspect; use read only with filters. Every write needs expectedHash = the stateHash from your latest read or " +
	"successful write. Writes are prepared first and applied only after approval."

func present(r json.RawMessage) bool { return len(r) > 0 && string(r) != "null" }

// elicitForm reports form-mode elicitation: an empty capability object
// (2025-06-18) or one listing "form" (2025-11-25).
func elicitForm(r json.RawMessage) bool {
	if !present(r) {
		return false
	}
	var caps map[string]json.RawMessage
	if json.Unmarshal(r, &caps) != nil {
		return false
	}
	if len(caps) == 0 {
		return true
	}
	_, form := caps["form"]
	return form
}

func (s *server) approvalMode() string {
	switch s.opts.WriteApproval {
	case ApproveAuto:
		if s.elicitation {
			return ApproveElicitation
		}
		return ApproveClient
	}
	return s.opts.WriteApproval
}

// call sends a request to the client and waits for its response.
func (s *server) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	s.mu.Lock()
	s.nextID++
	id := json.RawMessage(strconv.Quote("shiori-" + strconv.FormatInt(s.nextID, 10)))
	c := make(chan message, 1)
	s.pending[string(id)] = c
	s.mu.Unlock()
	req := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		req["params"] = params
	}
	s.write(req)
	select {
	case m, ok := <-c:
		if !ok {
			return nil, errors.New("the client closed the connection")
		}
		if m.Error != nil {
			return nil, fmt.Errorf("%s: %s", method, m.Error.Message)
		}
		return m.Result, nil
	case <-ctx.Done():
		s.mu.Lock()
		delete(s.pending, string(id))
		s.mu.Unlock()
		return nil, ctx.Err()
	}
}

// engineFor binds the session to one canonical project root.
func (s *server) engineFor(ctx context.Context) (*engine.Engine, error) {
	s.mu.Lock()
	e, stale, roots := s.engine, s.rootsStale, s.roots
	s.mu.Unlock()
	if e != nil && (s.opts.Root != "" || !stale) {
		return e, nil
	}
	root := s.opts.Root
	if root == "" {
		if !roots {
			return nil, errors.New("No project root: configure the server with --root <dir> (or use a client that provides MCP roots)")
		}
		raw, err := s.call(ctx, "roots/list", nil)
		if err != nil {
			return nil, fmt.Errorf("Could not list the client's roots: %v", err)
		}
		var res struct {
			Roots []struct {
				URI string `json:"uri"`
			} `json:"roots"`
		}
		if err := json.Unmarshal(raw, &res); err != nil {
			return nil, fmt.Errorf("roots/list: %v", err)
		}
		var dirs []string
		for _, r := range res.Roots {
			u, err := url.Parse(r.URI)
			if err == nil && u.Scheme == "file" && filepath.IsAbs(u.Path) {
				dirs = append(dirs, u.Path)
			}
		}
		if len(dirs) != 1 {
			return nil, fmt.Errorf("The client provides %d file roots (%s); configure the server with --root <dir>", len(dirs), strings.Join(dirs, ", "))
		}
		root = dirs[0]
	}
	ne, err := engine.New(root)
	if err != nil {
		return nil, err
	}
	ne.Compaction = s.opts.Compaction
	ne.JournalVersion = s.opts.JournalVersion
	ne.Rebase = s.opts.Rebase
	ne.Source = history.SourceMCP
	ne.Cache = snapshot.NewCache(snapshot.DefaultCacheBudget)
	s.mu.Lock()
	if s.engine != nil && s.engine.Root == ne.Root {
		ne = s.engine
	}
	s.engine, s.rootsStale = ne, false
	s.mu.Unlock()
	return ne, nil
}

// toolResult is a tools/call result: the exact tool text, or an error the
// model can act on.
func toolResult(text string, isError bool) map[string]any {
	return map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}, "isError": isError}
}

func (s *server) callTool(ctx context.Context, raw json.RawMessage) (any, *rpcError) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &rpcError{codeInvalidParams, "tools/call: " + err.Error()}
	}
	t, ok := s.byMCP[p.Name]
	if !ok {
		return nil, &rpcError{codeInvalidParams, "Unknown tool: " + p.Name}
	}
	start := time.Now()
	text, err := s.run(ctx, t, p.Arguments)
	class := "ok"
	if err != nil {
		class = engine.ErrorClass(err)
	}
	s.logf("%s -> %s (%s)", p.Name, class, time.Since(start).Round(time.Microsecond))
	if !readTools[strings.TrimPrefix(t.name, "workplan_")] && err == nil {
		s.checkChanges() // announce our own writes without waiting for the poll
	}
	if err != nil {
		return toolResult(err.Error(), true), nil
	}
	return toolResult(text, false), nil
}

func (s *server) run(ctx context.Context, t tool, rawArgs json.RawMessage) (string, error) {
	if len(rawArgs) == 0 || string(rawArgs) == "null" {
		rawArgs = json.RawMessage("{}")
	}
	parsed, err := ojson.Parse(rawArgs)
	if err != nil {
		return "", fmt.Errorf("Invalid tool arguments: %v", err)
	}
	args := parsed.Value
	e, err := s.engineFor(ctx)
	if err != nil {
		return "", err
	}
	short := strings.TrimPrefix(t.name, "workplan_")
	if readTools[short] && short != "compact_preview" {
		text, _, err := e.RunRead(short, args, s.runtimeFacts())
		return text, err
	}
	data, err := input.ParseMutationInput(short, args, input.SurfaceNative)
	if err != nil {
		return "", err
	}
	prep, err := e.Prepare(short, data)
	if err != nil {
		return "", err
	}
	var auth engine.Authorizer
	if prep.Intent != nil {
		if !protocol.PlatformSupported(runtime.GOOS, runtime.GOARCH) {
			return "", fmt.Errorf("Workplan writes are not supported on %s/%s by this core; read operations remain available", runtime.GOOS, runtime.GOARCH)
		}
		switch s.approvalMode() {
		case ApproveDeny:
			return "", errors.New("This workplan MCP server is read-only (--write-approval deny); nothing was changed")
		case ApproveElicitation:
			if !s.elicitation {
				return "", errors.New("Workplan writes need user approval through MCP elicitation, which this client does not support; nothing was changed")
			}
			auth = &elicitAuthorizer{s: s, root: e.Root}
		case ApproveClient:
			auth = clientApproved{}
		}
	}
	out, err := e.Execute(ctx, prep, auth, engine.ExecOptions{})
	if err != nil {
		return "", err
	}
	return out.String(), nil
}

// runtimeFacts are the doctor facts this server can state.
func (s *server) runtimeFacts() *ojson.Value {
	names := make([]string, len(s.tools))
	for i, t := range s.tools {
		names[i] = s.opts.ToolPrefix + strings.TrimPrefix(t.name, "workplan_")
	}
	s.mu.Lock()
	client := s.client
	s.mu.Unlock()
	v := ojson.NewObject(3).
		Set("registrations", ojson.NewObject(1).Set("effective", ojson.StringsValue(names)).Value()).
		Set("plugin", ojson.NewObject(3).
			Set("id", ojson.StringValue("shiori-mcp")).
			Set("configured", ojson.BoolValue(true)).
			Set("effective", ojson.BoolValue(true)).Value()).
		Set("permission", ojson.NewObject(1).
			Set("detail", ojson.StringValue("MCP client "+client+"; writes approved by "+s.approvalMode())).Value()).Value()
	return &v
}

// clientApproved: the client's tool-call approval authorized this call.
type clientApproved struct{}

func (clientApproved) Authorize(context.Context, engine.AuthRequest) error { return nil }

// elicitAuthorizer asks the user, through the client, to approve the exact
// prepared intent. No lock or file exists while the question is open.
type elicitAuthorizer struct {
	s    *server
	root string
}

func (a *elicitAuthorizer) Authorize(ctx context.Context, req engine.AuthRequest) error {
	params := map[string]any{
		"message": describe(req, a.root),
		"requestedSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"approve": map[string]any{"type": "boolean", "title": "Apply this workplan change",
					"description": "Writes exactly the files listed; nothing else changes."},
			},
			"required": []string{"approve"},
		},
	}
	a.s.mu.Lock()
	if a.s.version == "2025-11-25" {
		params["mode"] = "form"
	}
	a.s.mu.Unlock()
	raw, err := a.s.call(ctx, "elicitation/create", params)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%w (%v)", engine.ErrDenied, err)
	}
	var res struct {
		Action  string `json:"action"`
		Content struct {
			Approve bool `json:"approve"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &res); err != nil || res.Action != "accept" || !res.Content.Approve {
		return engine.ErrDenied
	}
	return nil
}

// describe is the approval prompt: the tool, the plan and every path the
// intent writes, deletes or archives, relative to the root.
func describe(req engine.AuthRequest, root string) string {
	rel := func(p string) string {
		if r, err := filepath.Rel(root, p); err == nil {
			return r
		}
		return p
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s on workplan %s (%s) in %s\n", req.Tool, req.Intent.WorkplanID, req.Intent.Operation, root)
	r := req.Resources
	for _, l := range []struct {
		label string
		paths []string
	}{{"write", r.Write}, {"delete", r.Delete}, {"archive", r.Archive}} {
		for _, p := range l.paths {
			fmt.Fprintf(&b, "  %s %s\n", l.label, rel(p))
		}
	}
	fmt.Fprintf(&b, "plus %d staging and %d lock-protocol files, a recovery journal; intent %s", len(r.Staging), len(r.Lock), req.Digest[:12])
	return b.String()
}
