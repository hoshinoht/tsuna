package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestServeUsage(t *testing.T) {
	var out, errb bytes.Buffer
	if code := runServe(nil, strings.NewReader(""), &out, &errb); code != 2 {
		t.Fatalf("serve without --stdio: exit %d", code)
	}
	if code := runServe([]string{"--stdio", "--idle-timeout", "0s"}, strings.NewReader(""), &out, &errb); code != 2 {
		t.Fatalf("zero idle timeout accepted: exit %d", code)
	}
}

func TestServeHandshakeThenEOF(t *testing.T) {
	var out, errb bytes.Buffer
	in := strings.NewReader(`{"type":"request","protocolVersion":1,"requestId":"hs","operation":"shiori.handshake","input":{"protocolVersions":[1]}}` + "\n")
	if code := runServe([]string{"--stdio"}, in, &out, &errb); code != 0 {
		t.Fatalf("exit %d; stderr %s", code, errb.String())
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 1 || !strings.Contains(lines[0], `"contractVersion":"v1"`) || !strings.Contains(lines[0], `"coreVersion":"`+Version+`"`) {
		t.Fatalf("stdout must carry exactly the handshake frame: %q", out.String())
	}
	if !strings.Contains(errb.String(), "stdin closed") {
		t.Fatalf("stderr: %q", errb.String())
	}
}
