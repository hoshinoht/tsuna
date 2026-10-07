package snapshot

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// WorkplanDir is the coordination root relative to the project root.
const WorkplanDir = ".opencode/workplan"

// CanonicalRoot resolves the trusted project root to an absolute path with
// symlinks evaluated (canonical roots come from trusted context, never
// model input).
func CanonicalRoot(root string) (string, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("Workspace root does not exist: %s", abs)
		}
		return "", err
	}
	return real, nil
}

// relInRoot normalizes a stored link (relative, "./"-prefixed or absolute
// in-root) to a clean project-relative path with '/' separators. It
// returns ok=false when the link escapes the root.
func relInRoot(root, raw string) (string, bool) {
	s := raw
	if filepath.IsAbs(s) {
		rel, err := filepath.Rel(root, filepath.Clean(s))
		if err != nil {
			return "", false
		}
		s = filepath.ToSlash(rel)
	}
	s = path.Clean(s)
	if s == "." || s == ".." || strings.HasPrefix(s, "../") || strings.HasPrefix(s, "/") {
		return "", false
	}
	return s, true
}

// NormalizePlanFile applies the linked-Markdown policy. The error texts
// are the reference's plan-file policy messages.
func NormalizePlanFile(root, raw string) (string, error) {
	rel, ok := relInRoot(root, raw)
	if !ok {
		return "", fmt.Errorf("Plan file must stay inside workspace root: %s", raw)
	}
	if !strings.HasPrefix(rel, WorkplanDir+"/") {
		return "", fmt.Errorf("Plan file must stay under .opencode/workplan/: %s", raw)
	}
	if !strings.HasSuffix(rel, ".md") {
		return "", fmt.Errorf("Plan file must be Markdown under .opencode/workplan/: %s", raw)
	}
	return rel, nil
}

// NormalizeSpecFile applies the linked-spec policy (inside the root).
func NormalizeSpecFile(root, raw string) (string, error) {
	rel, ok := relInRoot(root, raw)
	if !ok {
		return "", fmt.Errorf("Spec file must stay inside workspace root: %s", raw)
	}
	return rel, nil
}

// ManifestPath is the manifest identity of a project-relative path. A
// literal backslash is rewritten to '/' for hash parity with the
// reference; such paths are reported by validation and new ones are
// refused by writers.
func ManifestPath(rel string) string { return strings.ReplaceAll(rel, `\`, "/") }

// abs joins a project-relative path onto the root using the host
// separator. On POSIX a literal backslash stays part of the file name.
func abs(root, rel string) string { return filepath.Join(root, filepath.FromSlash(rel)) }

// withinRoot reports whether the fully resolved target stays inside root.
// Missing targets are accepted (their parent chain is checked instead).
func withinRoot(root, target string) bool {
	p := target
	for {
		real, err := filepath.EvalSymlinks(p)
		if err == nil {
			rel, err := filepath.Rel(root, real)
			return err == nil && rel != ".." && !strings.HasPrefix(rel, "../")
		}
		parent := filepath.Dir(p)
		if parent == p {
			return false
		}
		p = parent
	}
}

// CheckWritable refuses a write target that is not a clean in-root path,
// or whose existing path components (below the root) include a symlink:
// writers never follow or replace symlinks.
func CheckWritable(root, rel string) error {
	clean, ok := relInRoot(root, rel)
	if !ok || clean != rel {
		return fmt.Errorf("Workplan write target must be a clean path inside the workspace root: %s", rel)
	}
	p := root
	for _, seg := range strings.Split(rel, "/") {
		p = filepath.Join(p, seg)
		st, err := os.Lstat(p)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		if st.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("Refusing to write through a symlink: %s", p)
		}
	}
	return nil
}
