package schematest

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/hoshinoht/shiori/internal/protocol"
	"github.com/hoshinoht/shiori/internal/testutil"
)

// TestProtocolFramesMatchEnvelope validates every frame a real session
// produces (handshake, reads, prepared intent, commit, discard, errors)
// and every request the test sends against protocol-envelope-v1.
func TestProtocolFramesMatchEnvelope(t *testing.T) {
	c := compiler(t)
	sch, err := c.Compile(idBase + "protocol-envelope-v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	dst := t.TempDir()
	if err := testutil.CopyTree(testutil.Testdata("fixtures", "minimal-valid"), dst); err != nil {
		t.Fatal(err)
	}
	root, _ := filepath.EvalSymlinks(dst)

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	yes := true
	done := make(chan error, 1)
	go func() {
		done <- protocol.Serve(context.Background(), protocol.Options{In: inR, Out: outW, Err: io.Discard, CoreVersion: "test", IdleTimeout: -1, WriteSupported: &yes})
		outW.Close()
	}()
	lines := bufio.NewReader(outR)
	validate := func(what string, raw []byte) map[string]any {
		t.Helper()
		v, err := jsonschemaUnmarshal(raw)
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		if err := sch.Validate(v); err != nil {
			t.Fatalf("%s does not match protocol-envelope-v1: %v\n%s", what, err, raw)
		}
		var m map[string]any
		json.Unmarshal(raw, &m)
		return m
	}
	hc := map[string]any{"mode": "native", "canonicalRoot": root, "sessionID": "s", "agent": "plan", "messageID": "m", "callID": "c"}
	roundTrip := func(req map[string]any) map[string]any {
		t.Helper()
		b, _ := json.Marshal(req)
		validate("request", b)
		inW.Write(append(b, '\n'))
		line, err := lines.ReadBytes('\n')
		if err != nil {
			t.Fatal(err)
		}
		return validate("response", line)
	}
	req := func(id, op string, input map[string]any) map[string]any {
		r := map[string]any{"type": "request", "protocolVersion": 1, "requestId": id, "operation": op, "input": input}
		if op != "shiori.handshake" {
			r["hostContext"] = hc
		}
		return r
	}
	roundTrip(req("hs", "shiori.handshake", map[string]any{"client": "schema-test", "protocolVersions": []int{1}}))
	read := roundTrip(req("r", "workplan_read", map[string]any{"id": "minimal", "includeMarkdown": false}))
	var doc map[string]any
	json.Unmarshal([]byte(read["result"].(map[string]any)["text"].(string)), &doc)
	roundTrip(req("e", "workplan_read", map[string]any{"id": "minimal", "bogus": 1}))
	p := roundTrip(req("p", "workplan_update", map[string]any{"id": "minimal", "expectedHash": doc["stateHash"], "title": "schema"}))["prepared"].(map[string]any)
	roundTrip(req("c", "shiori.commit", map[string]any{"intentId": p["intentId"], "intentDigest": p["intentDigest"], "capability": p["capability"]}))
	roundTrip(req("d", "shiori.discard", map[string]any{"intentId": p["intentId"], "intentDigest": p["intentDigest"]}))
	cancel, _ := json.Marshal(map[string]any{"type": "cancel", "protocolVersion": 1, "requestId": "zz"})
	validate("cancel", cancel)
	inW.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not exit")
	}
}

func jsonschemaUnmarshal(b []byte) (any, error) {
	return jsonschemaUnmarshalReader(strings.NewReader(string(b)))
}

func jsonschemaUnmarshalReader(r io.Reader) (any, error) { return jsonschema.UnmarshalJSON(r) }
