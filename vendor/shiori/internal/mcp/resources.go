package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/hoshinoht/shiori/internal/engine"
	"github.com/hoshinoht/shiori/internal/ojson"
)

// Resources: each plan's resume packet, status report and change log, at
// workplan://<id>/<kind>. Subscribed resources are polled for changes
// (state hash, evidence, lanes, change log) and announced with
// notifications/resources/updated; a changed set of plans with
// notifications/resources/list_changed once the client has listed them.

var resourceKinds = []struct {
	kind, mime, title, description string
}{
	{"resume", "application/json", "resume", "Bounded continuation packet: current step, active work, high findings, writes since the last checkpoint."},
	{"report", "text/markdown", "status report", "Progress, open work, evidence with the commits it verified, findings, lanes and recent activity."},
	{"history", "application/json", "change log", "The newest 50 logged writes: operation, source, hashes and the plan elements each changed."},
}

// DefaultPollInterval is how often subscribed plans are checked.
const DefaultPollInterval = 2 * time.Second

func resourceURI(id, kind string) string { return "workplan://" + id + "/" + kind }

func parseResourceURI(uri string) (id, kind string, ok bool) {
	rest, found := strings.CutPrefix(uri, "workplan://")
	if !found {
		return "", "", false
	}
	id, kind, found = strings.Cut(rest, "/")
	if !found || id == "" {
		return "", "", false
	}
	for _, k := range resourceKinds {
		if k.kind == kind {
			return id, kind, true
		}
	}
	return "", "", false
}

func (s *server) listResources(ctx context.Context) (any, *rpcError) {
	e, err := s.engineFor(ctx)
	if err != nil {
		return nil, &rpcError{codeInternal, err.Error()}
	}
	ids, err := e.PlanIDs()
	if err != nil {
		return nil, &rpcError{codeInternal, err.Error()}
	}
	list := []any{}
	for _, id := range ids {
		for _, k := range resourceKinds {
			list = append(list, map[string]any{"uri": resourceURI(id, k.kind), "name": id + " " + k.title, "mimeType": k.mime, "description": k.description})
		}
	}
	s.mu.Lock()
	s.listedPlans = strings.Join(ids, "\x00")
	s.listed = true
	s.mu.Unlock()
	s.startWatch()
	return map[string]any{"resources": list}, nil
}

func listTemplates() any {
	list := []any{}
	for _, k := range resourceKinds {
		list = append(list, map[string]any{"uriTemplate": "workplan://{id}/" + k.kind, "name": "plan " + k.title, "mimeType": k.mime, "description": k.description})
	}
	return map[string]any{"resourceTemplates": list}
}

func (s *server) readResource(ctx context.Context, raw json.RawMessage) (any, *rpcError) {
	var p struct {
		URI string `json:"uri"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &rpcError{codeInvalidParams, "resources/read: " + err.Error()}
	}
	id, kind, ok := parseResourceURI(p.URI)
	if !ok {
		return nil, &rpcError{codeResourceNotFound, "Resource not found: " + p.URI}
	}
	e, err := s.engineFor(ctx)
	if err != nil {
		return nil, &rpcError{codeInternal, err.Error()}
	}
	text, mime, err := resourceText(ctx, e, id, kind, s.runtimeFacts())
	if err != nil {
		return nil, &rpcError{codeResourceNotFound, err.Error()}
	}
	return map[string]any{"contents": []any{map[string]any{"uri": p.URI, "mimeType": mime, "text": text}}}, nil
}

func resourceText(ctx context.Context, e *engine.Engine, id, kind string, facts *ojson.Value) (string, string, error) {
	switch kind {
	case "resume":
		args := ojson.NewObject(1).Set("id", ojson.StringValue(id)).Value()
		text, _, err := e.RunRead("resume", args, facts)
		return text, "application/json", err
	case "report":
		_, md, err := e.Report(ctx, id, 20)
		return md, "text/markdown", err
	default:
		v, err := e.History(id, "", 50)
		return string(ojson.Pretty(v)), "application/json", err
	}
}

func (s *server) subscribe(raw json.RawMessage, on bool) (any, *rpcError) {
	var p struct {
		URI string `json:"uri"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &rpcError{codeInvalidParams, "resources/subscribe: " + err.Error()}
	}
	id, _, ok := parseResourceURI(p.URI)
	if !ok {
		return nil, &rpcError{codeResourceNotFound, "Resource not found: " + p.URI}
	}
	s.mu.Lock()
	if on {
		s.subs[p.URI] = id
	} else {
		delete(s.subs, p.URI)
	}
	s.mu.Unlock()
	if on {
		s.startWatch()
		s.checkChanges() // records the current token
	}
	return map[string]any{}, nil
}

// startWatch starts the poller once.
func (s *server) startWatch() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.watching || s.ctx == nil {
		return
	}
	s.watching = true
	interval := s.opts.PollInterval
	if interval <= 0 {
		interval = DefaultPollInterval
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-s.ctx.Done():
				return
			case <-t.C:
				s.checkChanges()
			}
		}
	}()
}

// checkChanges announces subscribed resources whose plan changed and a
// changed plan set.
func (s *server) checkChanges() {
	s.checkMu.Lock()
	defer s.checkMu.Unlock()
	s.mu.Lock()
	e := s.engine
	subs := map[string]string{}
	for uri, id := range s.subs {
		subs[uri] = id
	}
	listed, listedPlans := s.listed, s.listedPlans
	s.mu.Unlock()
	if e == nil || s.ctx.Err() != nil {
		return
	}
	tokens := map[string]string{}
	for uri, id := range subs {
		tok, ok := tokens[id]
		if !ok {
			tok = e.ChangeToken(id)
			tokens[id] = tok
		}
		s.mu.Lock()
		prev, seen := s.tokens[uri]
		s.tokens[uri] = tok
		s.mu.Unlock()
		if seen && prev != tok {
			s.write(map[string]any{"jsonrpc": "2.0", "method": "notifications/resources/updated", "params": map[string]any{"uri": uri}})
		}
	}
	if listed {
		if ids, err := e.PlanIDs(); err == nil {
			if cur := strings.Join(ids, "\x00"); cur != listedPlans {
				s.mu.Lock()
				s.listedPlans = cur
				s.mu.Unlock()
				s.write(map[string]any{"jsonrpc": "2.0", "method": "notifications/resources/list_changed"})
			}
		}
	}
}
