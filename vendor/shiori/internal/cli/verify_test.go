package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/hoshinoht/shiori/internal/testutil"
)

func TestShellSplitInvertsJoin(t *testing.T) {
	for _, argv := range [][]string{
		{"go", "test", "./..."},
		{"sh", "-c", "test -f 'a b' && echo \"ok\""},
		{"printf", "%s\n", "it's", ""},
		{"ünïcode", "x=1"},
	} {
		got, err := shellSplit(shellJoin(argv))
		if err != nil || !reflect.DeepEqual(got, argv) {
			t.Fatalf("%q -> %q -> %q (%v)", argv, shellJoin(argv), got, err)
		}
	}
	for _, bad := range []string{"", "a;b", `echo "x"`, "'open", "$(rm -rf x)"} {
		if _, err := shellSplit(bad); err == nil {
			t.Fatalf("%q accepted", bad)
		}
	}
}

// TestVerifyRerunsStaleEvidence: a command recorded with evidence --
// COMMAND is re-run once its scope changed; asserted records are listed
// but never run.
func TestVerifyRerunsStaleEvidence(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := testutil.NewRoot(t, "full-valid")
	withPrompt(t, false, "")
	os.MkdirAll(filepath.Join(root.Path, "src"), 0o755)
	os.WriteFile(filepath.Join(root.Path, "src/a.txt"), []byte("fine\n"), 0o644)
	git := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = root.Path
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	git("add", "-A")
	git("commit", "-qm", "init")
	hash := func() string {
		_, out, _ := run("read", "full-plan", "--json", "--no-markdown", "--root", root.Path)
		var v struct{ StateHash string }
		json.Unmarshal([]byte(out), &v)
		return v.StateHash
	}
	if code, _, errOut := run("evidence", "full-plan", "--phase", "phase-b", "--step", "step-b1", "--scope", "src", "--expected-hash", hash(), "--yes", "--root", root.Path,
		"--", "grep", "-q", "fine", "src/a.txt"); code != 0 {
		t.Fatal(errOut)
	}
	if code, _, errOut := run("evidence", "full-plan", "--phase", "phase-b", "--step", "step-b1", "--scope", "src", "--command", "make lint", "--exit-code", "0", "--expected-hash", hash(), "--yes", "--root", root.Path); code != 0 {
		t.Fatal(errOut)
	}
	if code, out, _ := run("verify", "full-plan", "--root", root.Path); code != 0 || !strings.Contains(out, "Nothing to re-run") {
		t.Fatalf("fresh evidence re-run: %d %s", code, out)
	}
	os.WriteFile(filepath.Join(root.Path, "src/a.txt"), []byte("bad\n"), 0o644)
	code, _, errOut := run("verify", "full-plan", "--dry-run", "--root", root.Path)
	if code != 0 || !strings.Contains(errOut, "phase-b/step-b1 [stale]: grep -q fine src/a.txt") || !strings.Contains(errOut, "make lint (recorded by cli)") {
		t.Fatalf("dry run: %d %s", code, errOut)
	}
	if code, _, errOut := run("verify", "full-plan", "--root", root.Path); code != 1 || !strings.Contains(errOut, "pass --yes") {
		t.Fatalf("without --yes off a terminal: %d %s", code, errOut)
	}
	code, out, errOut := run("verify", "full-plan", "--yes", "--json", "--root", root.Path)
	var res struct {
		Results []struct {
			Command       string
			ExitCode      int
			PreviousState string
		}
	}
	json.Unmarshal([]byte(out), &res)
	if code != 1 || len(res.Results) != 1 || res.Results[0].ExitCode != 1 || res.Results[0].PreviousState != "stale" {
		t.Fatalf("verify: %d %s %s", code, out, errOut)
	}
	// The failing re-run is now the step's latest record.
	if _, out, _ := run("verify", "full-plan", "--dry-run", "--root", root.Path); out != "" {
		t.Fatalf("dry run printed results: %s", out)
	}
	if _, _, errOut := run("verify", "full-plan", "--dry-run", "--root", root.Path); !strings.Contains(errOut, "[failing]") {
		t.Fatalf("after a failing re-run: %s", errOut)
	}
}

func TestCreateFromTemplate(t *testing.T) {
	root := t.TempDir()
	withPrompt(t, false, "")
	if code, _, errOut := run("create", "fix-it", "--goal", "Fix it", "--template", "bugfix", "--yes", "--root", root); code != 0 {
		t.Fatal(errOut)
	}
	_, out, _ := run("read", "fix-it", "--json", "--no-markdown", "--root", root)
	for _, want := range []string{`"kind": "bugfix"`, `"id": "failing-test"`, "`TEST_COMMAND` fails with the reported symptom"} {
		if !strings.Contains(out, want) {
			t.Fatalf("plan lacks %s:\n%s", want, out)
		}
	}
	if code, _, errOut := run("create", "x", "--goal", "g", "--template", "nope", "--root", root); code != 2 || !strings.Contains(errOut, "bugfix, feature, migration") {
		t.Fatalf("unknown template: %d %s", code, errOut)
	}
	if code, _, errOut := run("create", "y", "--goal", "g", "--template", "feature", "--input", `{"phases":[]}`, "--root", root); code != 2 || !strings.Contains(errOut, "exclusive") {
		t.Fatalf("template with phases: %d %s", code, errOut)
	}
}
