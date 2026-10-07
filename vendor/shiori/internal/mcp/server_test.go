package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hoshinoht/shiori/internal/engine"
	"github.com/hoshinoht/shiori/internal/input"
	"github.com/hoshinoht/shiori/internal/ojson"
	"github.com/hoshinoht/shiori/internal/testutil"
)

// client is a scripted MCP client on the other end of the pipes.
type client struct {
	t      *testing.T
	in     *io.PipeWriter
	out    *bufio.Scanner
	done   chan error
	stderr *bytes.Buffer
	// answer replies to server requests (roots/list, elicitation/create).
	answer func(method string, params json.RawMessage) any
	nextID int
	notes  []string // notification methods (and uris) received
}

func start(t *testing.T, opts Options) *client {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	c := &client{t: t, in: inW, out: bufio.NewScanner(outR), done: make(chan error, 1), stderr: &bytes.Buffer{}}
	c.out.Buffer(make([]byte, 1<<20), 64<<20)
	opts.In, opts.Out, opts.Err = inR, outW, c.stderr
	go func() { c.done <- Serve(context.Background(), opts); outW.Close() }()
	t.Cleanup(func() {
		inW.Close()
		go func() {
			for c.out.Scan() {
			}
		}()
		select {
		case <-c.done:
		case <-time.After(5 * time.Second):
			t.Error("server did not exit")
		}
	})
	return c
}

func (c *client) send(v any) {
	data, _ := json.Marshal(v)
	c.in.Write(append(data, '\n'))
}

// next reads the next message, answering server requests on the way.
func (c *client) next() map[string]json.RawMessage {
	c.t.Helper()
	for c.out.Scan() {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(c.out.Bytes(), &m); err != nil {
			c.t.Fatalf("bad frame %s: %v", c.out.Bytes(), err)
		}
		if method, ok := m["method"]; ok {
			var name string
			json.Unmarshal(method, &name)
			if _, isRequest := m["id"]; !isRequest {
				var p struct{ URI string }
				json.Unmarshal(m["params"], &p)
				c.notes = append(c.notes, strings.TrimSpace(name+" "+p.URI))
				continue
			}
			if c.answer == nil {
				c.t.Fatalf("unexpected server request %s", name)
			}
			c.send(map[string]any{"jsonrpc": "2.0", "id": m["id"], "result": c.answer(name, m["params"])})
			continue
		}
		return m
	}
	c.t.Fatal("server output closed")
	return nil
}

func (c *client) call(method string, params any) map[string]json.RawMessage {
	c.t.Helper()
	c.nextID++
	c.send(map[string]any{"jsonrpc": "2.0", "id": c.nextID, "method": method, "params": params})
	return c.next()
}

func (c *client) initialize(caps map[string]any) map[string]any {
	c.t.Helper()
	m := c.call("initialize", map[string]any{"protocolVersion": "2025-06-18", "capabilities": caps, "clientInfo": map[string]any{"name": "test", "version": "1"}})
	c.send(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	var res map[string]any
	json.Unmarshal(m["result"], &res)
	return res
}

type toolOut struct {
	Content []struct {
		Text string `json:"text"`
	} `json:"content"`
	IsError bool `json:"isError"`
}

func (c *client) tool(name string, args any) toolOut {
	c.t.Helper()
	m := c.call("tools/call", map[string]any{"name": name, "arguments": args})
	if e, ok := m["error"]; ok {
		c.t.Fatalf("%s: rpc error %s", name, e)
	}
	var out toolOut
	json.Unmarshal(m["result"], &out)
	return out
}

func stateHash(t *testing.T, root, id string) string {
	t.Helper()
	e, _ := engine.New(root)
	_, text, err := e.Resume(input.ResumeInput{ID: id})
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Hashes struct {
			StateHash string `json:"stateHash"`
		} `json:"hashes"`
	}
	json.Unmarshal([]byte(text), &v)
	return v.Hashes.StateHash
}

func TestRegistrationMatchesAdapter(t *testing.T) {
	adapter, err := os.ReadFile(filepath.Join(testutil.RepoRoot(), "adapter", "opencode", "src", "registration.json"))
	if err != nil {
		t.Skip("adapter not present")
	}
	if !bytes.Equal(adapter, Registration) {
		t.Fatal("internal/mcp/registration.json differs from the adapter's registration.json; copy it")
	}
}

func TestHandshakeAndToolList(t *testing.T) {
	c := start(t, Options{Root: t.TempDir()})
	res := c.initialize(map[string]any{})
	if res["protocolVersion"] != "2025-06-18" {
		t.Fatalf("negotiated %v", res["protocolVersion"])
	}
	m := c.call("tools/list", map[string]any{})
	var list struct {
		Tools []struct {
			Name        string          `json:"name"`
			InputSchema json.RawMessage `json:"inputSchema"`
			Annotations map[string]any  `json:"annotations"`
		} `json:"tools"`
	}
	json.Unmarshal(m["result"], &list)
	if len(list.Tools) != 13 {
		t.Fatalf("%d tools", len(list.Tools))
	}
	names := map[string]bool{}
	for _, tl := range list.Tools {
		names[tl.Name] = true
		if !bytes.HasPrefix(tl.InputSchema, []byte("{")) {
			t.Fatalf("%s: schema %s", tl.Name, tl.InputSchema)
		}
	}
	for _, n := range []string{"resume", "update", "compact_preview", "doctor"} {
		if !names[n] {
			t.Fatalf("missing tool %s in %v", n, names)
		}
	}
	for _, tl := range list.Tools {
		if (tl.Name == "resume" || tl.Name == "compact_preview") && tl.Annotations["readOnlyHint"] != true {
			t.Fatalf("%s not read-only", tl.Name)
		}
		if tl.Name == "update" && tl.Annotations["readOnlyHint"] != false {
			t.Fatalf("update read-only")
		}
	}
	// An unknown version gets the newest; errors use JSON-RPC codes.
	m = c.call("initialize", map[string]any{"protocolVersion": "1999-01-01", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "x"}})
	if !strings.Contains(string(m["result"]), Versions[0]) {
		t.Fatalf("version %s", m["result"])
	}
	if m = c.call("nope", nil); !strings.Contains(string(m["error"]), "-32601") {
		t.Fatalf("unknown method %s", m["error"])
	}
	if m = c.call("tools/call", map[string]any{"name": "workplan_resume"}); !strings.Contains(string(m["error"]), "-32602") {
		t.Fatalf("unknown tool %s", m["error"])
	}
}

func TestReadsMatchTheEngine(t *testing.T) {
	root := testutil.NewRoot(t, "full-valid")
	c := start(t, Options{Root: root.Path, ToolPrefix: "workplan_"})
	c.initialize(map[string]any{})
	got := c.tool("workplan_resume", map[string]any{"id": "full-plan"})
	e, _ := engine.New(root.Path)
	_, want, _ := e.Resume(input.ResumeInput{ID: "full-plan"})
	if got.IsError || got.Content[0].Text != want {
		t.Fatalf("resume differs:\n%s", testutil.FirstDiff(got.Content[0].Text, want))
	}
	bad := c.tool("workplan_read", map[string]any{"id": "full-plan", "workspaceRoot": "/etc"})
	if !bad.IsError || !strings.Contains(bad.Content[0].Text, "workspaceRoot") {
		t.Fatalf("workspaceRoot accepted: %+v", bad)
	}
}

func TestRootsFromTheClient(t *testing.T) {
	root := testutil.NewRoot(t, "full-valid")
	c := start(t, Options{})
	roots := []any{map[string]any{"uri": "file://" + root.Path, "name": "p"}}
	c.answer = func(method string, _ json.RawMessage) any {
		if method != "roots/list" {
			t.Fatalf("unexpected %s", method)
		}
		return map[string]any{"roots": roots}
	}
	c.initialize(map[string]any{"roots": map[string]any{"listChanged": true}})
	if out := c.tool("doctor", map[string]any{}); out.IsError || !strings.Contains(out.Content[0].Text, "shiori-mcp") {
		t.Fatalf("doctor: %+v", out)
	}
	roots = append(roots, map[string]any{"uri": "file:///tmp"})
	c.send(map[string]any{"jsonrpc": "2.0", "method": "notifications/roots/list_changed"})
	if out := c.tool("list", map[string]any{}); !out.IsError || !strings.Contains(out.Content[0].Text, "2 file roots") {
		t.Fatalf("two roots accepted: %+v", out)
	}
	// Without --root and without roots there is no root.
	c2 := start(t, Options{})
	c2.initialize(map[string]any{})
	if out := c2.tool("list", map[string]any{}); !out.IsError || !strings.Contains(out.Content[0].Text, "--root") {
		t.Fatalf("no root: %+v", out)
	}
}

func appendNote(c *client, root, note string) toolOut {
	return c.tool("update", map[string]any{"id": "full-plan", "expectedHash": stateHash(c.t, root, "full-plan"), "appendNotes": []string{note}})
}

func planHas(t *testing.T, root, s string) bool {
	data, err := os.ReadFile(filepath.Join(root, ".opencode/workplan/full-plan.json"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Contains(string(data), s)
}

func TestWritesThroughElicitation(t *testing.T) {
	root := testutil.NewRoot(t, "full-valid")
	c := start(t, Options{Root: root.Path})
	var asked string
	approve := true
	c.answer = func(method string, params json.RawMessage) any {
		if method != "elicitation/create" {
			t.Fatalf("unexpected %s", method)
		}
		var p struct {
			Message string `json:"message"`
		}
		json.Unmarshal(params, &p)
		asked = p.Message
		if !approve {
			return map[string]any{"action": "decline"}
		}
		return map[string]any{"action": "accept", "content": map[string]any{"approve": true}}
	}
	c.initialize(map[string]any{"elicitation": map[string]any{}})
	if out := appendNote(c, root.Path, "approved note"); out.IsError || !planHas(t, root.Path, "approved note") {
		t.Fatalf("approved write: %+v", out)
	}
	if !strings.Contains(asked, "write .opencode/workplan/full-plan.json") {
		t.Fatalf("prompt does not name the files:\n%s", asked)
	}
	approve = false
	if out := appendNote(c, root.Path, "declined note"); !out.IsError || !strings.Contains(out.Content[0].Text, "not authorized") || planHas(t, root.Path, "declined note") {
		t.Fatalf("declined write: %+v", out)
	}
	if m, _ := filepath.Glob(filepath.Join(root.Path, ".opencode/workplan/.*")); len(m) != 0 {
		t.Fatalf("a declined write left files: %v", m)
	}
}

func TestWriteApprovalModes(t *testing.T) {
	root := testutil.NewRoot(t, "full-valid")
	// auto without elicitation: the client's tool approval authorizes.
	c := start(t, Options{Root: root.Path})
	c.initialize(map[string]any{})
	if out := appendNote(c, root.Path, "client approved"); out.IsError || !planHas(t, root.Path, "client approved") {
		t.Fatalf("client approval: %+v", out)
	}
	for mode, want := range map[string]string{ApproveDeny: "read-only", ApproveElicitation: "does not support"} {
		c := start(t, Options{Root: root.Path, WriteApproval: mode})
		c.initialize(map[string]any{})
		if out := appendNote(c, root.Path, "refused "+mode); !out.IsError || !strings.Contains(out.Content[0].Text, want) || planHas(t, root.Path, "refused "+mode) {
			t.Fatalf("%s: %+v", mode, out)
		}
		// Reads still work.
		if out := c.tool("resume", map[string]any{"id": "full-plan"}); out.IsError {
			t.Fatalf("%s read: %+v", mode, out)
		}
	}
}

func TestCancelWhileAwaitingApproval(t *testing.T) {
	root := testutil.NewRoot(t, "full-valid")
	c := start(t, Options{Root: root.Path})
	c.initialize(map[string]any{"elicitation": map[string]any{}})
	hash := stateHash(t, root.Path, "full-plan")
	c.nextID++
	id := c.nextID
	c.send(map[string]any{"jsonrpc": "2.0", "id": id, "method": "tools/call", "params": map[string]any{
		"name": "update", "arguments": map[string]any{"id": "full-plan", "expectedHash": hash, "appendNotes": []string{"cancelled note"}}}})
	// The server asks; instead of answering, the client cancels the call.
	if !c.out.Scan() || !strings.Contains(c.out.Text(), "elicitation/create") {
		t.Fatalf("expected an elicitation, got %s", c.out.Text())
	}
	c.send(map[string]any{"jsonrpc": "2.0", "method": "notifications/cancelled", "params": map[string]any{"requestId": id}})
	// The next response is the ping's: the cancelled call never answers.
	if m := c.call("ping", nil); string(m["id"]) != strconv.Itoa(c.nextID) {
		t.Fatalf("expected the ping response, got %s", m["id"])
	}
	if planHas(t, root.Path, "cancelled note") {
		t.Fatal("a cancelled call wrote")
	}
	if m, _ := filepath.Glob(filepath.Join(root.Path, ".opencode/workplan/.*")); len(m) != 0 {
		t.Fatalf("a cancelled call left files: %v", m)
	}
}

// TestResources: plans are listed and readable as resources; subscribed
// ones are announced after our own writes and after changes made outside
// the session, and a new plan changes the list.
func TestResources(t *testing.T) {
	root := testutil.NewRoot(t, "full-valid")
	c := start(t, Options{Root: root.Path, WriteApproval: ApproveClient, PollInterval: 20 * time.Millisecond})
	info := c.initialize(map[string]any{})
	caps, _ := info["capabilities"].(map[string]any)
	if res, _ := caps["resources"].(map[string]any); res["subscribe"] != true || res["listChanged"] != true {
		t.Fatalf("capabilities %v", caps)
	}
	var list struct {
		Resources []struct{ URI, MimeType string }
	}
	json.Unmarshal(c.call("resources/list", nil)["result"], &list)
	uris := map[string]string{}
	for _, r := range list.Resources {
		uris[r.URI] = r.MimeType
	}
	if uris["workplan://full-plan/resume"] != "application/json" || uris["workplan://full-plan/report"] != "text/markdown" || uris["workplan://full-plan/history"] == "" {
		t.Fatalf("resources %v", uris)
	}
	var read struct {
		Contents []struct{ Text string }
	}
	json.Unmarshal(c.call("resources/read", map[string]any{"uri": "workplan://full-plan/report"})["result"], &read)
	if len(read.Contents) != 1 || !strings.Contains(read.Contents[0].Text, "# Full plan") {
		t.Fatalf("report %+v", read)
	}
	if m := c.call("resources/read", map[string]any{"uri": "workplan://full-plan/nope"}); !strings.Contains(string(m["error"]), "-32002") {
		t.Fatalf("unknown resource: %s", m["error"])
	}
	c.call("resources/subscribe", map[string]any{"uri": "workplan://full-plan/resume"})
	c.tool("update", map[string]any{"id": "full-plan", "expectedHash": stateHash(t, root.Path, "full-plan"), "appendNotes": []string{"from the session"}})
	if !slices.Contains(c.notes, "notifications/resources/updated workplan://full-plan/resume") {
		t.Fatalf("own write not announced: %v", c.notes)
	}
	wait := func(want string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for !slices.Contains(c.notes, want) {
			if time.Now().After(deadline) {
				t.Fatalf("no %q: %v", want, c.notes)
			}
			time.Sleep(10 * time.Millisecond)
			c.call("ping", nil)
		}
	}
	c.notes = nil
	e, _ := engine.New(root.Path)
	in, _ := ojson.Parse([]byte(`{"id":"other","goal":"g"}`))
	data, _ := input.ParseMutationInput("create", in.Value, input.SurfaceCore)
	p, err := e.Prepare("create", data)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Execute(context.Background(), p, allow{}, engine.ExecOptions{}); err != nil {
		t.Fatal(err)
	}
	wait("notifications/resources/list_changed")
	c.notes = nil
	pj := filepath.Join(root.Path, ".opencode/workplan/full-plan.json")
	b, _ := os.ReadFile(pj)
	os.WriteFile(pj, []byte(strings.Replace(string(b), `"goal": "`, `"goal": "edited `, 1)), 0o644)
	wait("notifications/resources/updated workplan://full-plan/resume")
}

type allow struct{}

func (allow) Authorize(context.Context, engine.AuthRequest) error { return nil }
