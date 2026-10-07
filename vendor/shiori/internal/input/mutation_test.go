package input

import (
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/hoshinoht/shiori/internal/ojson"
)

// TestNativeHashGuidance: every native existing-state writer refuses a
// missing expectedHash with the reference sentence followed by guidance
// that echoes no hash; the core surface keeps the reference text.
func TestNativeHashGuidance(t *testing.T) {
	lead := "Native existing-state writes require the current stateHash"
	hex64 := regexp.MustCompile(`[0-9a-f]{64}`)
	cases := map[string]string{
		"update":     `{"id":"x","title":"t"}`,
		"patch":      `{"id":"x","patchText":"p"}`,
		"reset":      `{"id":"x","mode":"draft"}`,
		"checkpoint": `{"id":"x","summary":"s","nextAction":"n"}`,
		"compact":    `{"id":"x","mode":"apply","archiveReason":"r","confirmation":"ARCHIVE_SELECTED_HISTORY","previewToken":"t"}`,
		"create":     `{"id":"x","goal":"g","overwrite":true}`,
	}
	for tool, in := range cases {
		v, err := ojson.Parse([]byte(in))
		if err != nil {
			t.Fatal(err)
		}
		_, err = ParseMutationInput(tool, v.Value, SurfaceNative)
		if err == nil {
			t.Fatalf("%s: accepted without expectedHash", tool)
		}
		var ie *InputError
		want := "expectedHash: " + lead + nativeHashGuidance
		if !strings.Contains(err.Error(), want) || !errors.As(err, &ie) {
			t.Fatalf("%s: %v, want an input error with %q", tool, err, want)
		}
		if hex64.MatchString(err.Error()) {
			t.Fatalf("%s: message echoes a hash: %v", tool, err)
		}
		// The core surface keeps its reference text.
		if _, err := ParseMutationInput(tool, v.Value, SurfaceCore); err != nil && strings.Contains(err.Error(), nativeHashGuidance) {
			t.Fatalf("%s: core surface got the native guidance: %v", tool, err)
		}
	}
}
