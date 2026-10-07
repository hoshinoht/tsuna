package resume

import (
	"testing"
)

func TestCursorRoundTrip(t *testing.T) {
	pid := "phase-2"
	c := Cursor{StateHash: "ef23629bf762ef3fa0f23594a009fc0391f50335875d3ceb26b44d4114884e69", MaxChars: 12000, Limit: 3, PhaseID: &pid, Offset: 3}
	tok := c.Encode()
	got, err := ParseCursor(tok)
	if err != nil {
		t.Fatal(err)
	}
	if got.Offset != 3 || got.Limit != 3 || got.MaxChars != 12000 || !EqualPtr(got.PhaseID, FilterDigest(&pid)) || got.StepID != nil {
		t.Fatalf("round trip %+v", got)
	}
	// A cursor is domain-bound: an inspect cursor never parses as resume.
	ic := InspectCursor{StateHash: c.StateHash, Limit: 3, Offset: 3}
	if _, err := ParseCursor(ic.Encode()); err == nil {
		t.Fatal("cross-domain cursor accepted")
	}
}

// FuzzCursorDecode: arbitrary tokens never panic; any accepted token
// re-encodes to itself (so no two tokens alias one position).
func FuzzCursorDecode(f *testing.F) {
	h := "ef23629bf762ef3fa0f23594a009fc0391f50335875d3ceb26b44d4114884e69"
	f.Add(InspectCursor{StateHash: h, Limit: 50, Offset: 50}.Encode())
	f.Add(Cursor{StateHash: h, MaxChars: 4096, Limit: 5, Offset: 5}.Encode())
	f.Add("!!not-base64!!")
	f.Add("")
	f.Add("eyJ2ZXJzaW9uIjoxfQ")
	f.Fuzz(func(t *testing.T, tok string) {
		if c, err := ParseInspectCursor(tok); err == nil {
			if again := c.Encode(); again != tok {
				t.Fatalf("inspect token %q re-encodes as %q", tok, again)
			}
		}
		if c, err := ParseCursor(tok); err == nil {
			// Filters are stored as digests; re-encode from stored values.
			fields := c.fields()
			fields[4].Value = ptrValue(c.PhaseID)
			fields[5].Value = ptrValue(c.StepID)
			if again := encodeCursor(resumeDomain, fields); again != tok {
				t.Fatalf("resume token %q re-encodes as %q", tok, again)
			}
		}
	})
}
