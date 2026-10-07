package protocol

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/hoshinoht/shiori/internal/ojson"
)

// Diagnostics go to stderr only, one line each, with redaction: issued
// intent capabilities, HTTP credentials and secret-looking assignments
// never appear.
var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(basic|bearer)\s+[A-Za-z0-9+/=._~-]+`),
	regexp.MustCompile(`(?i)\b(password|passwd|secret|token|capability|api[_-]?key)(["']?\s*[:=]\s*["']?)[^\s"',}]+`),
}

// Redact removes known secrets and secret-looking values from s.
func Redact(s string, secrets []string) string {
	for _, sec := range secrets {
		if sec != "" {
			s = strings.ReplaceAll(s, sec, "[redacted]")
		}
	}
	s = secretPatterns[0].ReplaceAllString(s, "$1 [redacted]")
	s = secretPatterns[1].ReplaceAllString(s, "$1$2[redacted]")
	return s
}

func (s *server) logf(format string, a ...any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logfLocked(format, a...)
}

func (s *server) logfLocked(format string, a ...any) {
	msg := Redact(fmt.Sprintf(format, a...), s.secrets)
	msg = strings.ReplaceAll(msg, "\n", " ")
	fmt.Fprintf(s.errw, "shiori serve: %s\n", msg)
}

// Durability is the handshake's observed durability facts.
type Durability struct {
	AtomicRename  string
	FileSync      string
	DirectorySync string
	ObservedAt    string
}

// Value renders the facts.
func (d Durability) Value() ojson.Value {
	return ojson.NewObject(4).
		Set("atomicRename", ojson.StringValue(d.AtomicRename)).
		Set("fileSync", ojson.StringValue(d.FileSync)).
		Set("directorySync", ojson.StringValue(d.DirectorySync)).
		Set("observedAt", ojson.StringValue(d.ObservedAt)).Value()
}

// ProbeDurability observes rename, file fsync and directory fsync in a
// private temporary directory (never the project root). A capability it
// cannot exercise is reported "unknown", never assumed. The project's
// own filesystem is reported per commit (directorySync in mutation
// results).
func ProbeDurability() Durability {
	d := Durability{AtomicRename: "unknown", FileSync: "unknown", DirectorySync: "unknown", ObservedAt: "private-temp-directory"}
	dir, err := os.MkdirTemp("", "shiori-durability-")
	if err != nil {
		return d
	}
	defer os.RemoveAll(dir)
	a := filepath.Join(dir, "a")
	f, err := os.OpenFile(a, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return d
	}
	_, werr := f.Write([]byte(time.Now().UTC().Format(time.RFC3339Nano)))
	serr := f.Sync()
	f.Close()
	if werr == nil {
		if serr == nil {
			d.FileSync = "supported"
		} else {
			d.FileSync = "unsupported"
		}
	}
	if err := os.Rename(a, filepath.Join(dir, "b")); err == nil {
		d.AtomicRename = "supported"
	} else {
		d.AtomicRename = "unsupported"
	}
	if df, err := os.Open(dir); err == nil {
		if df.Sync() == nil {
			d.DirectorySync = "supported"
		} else {
			d.DirectorySync = "unsupported"
		}
		df.Close()
	}
	return d
}
