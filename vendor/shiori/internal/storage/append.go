package storage

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"

	"github.com/hoshinoht/shiori/internal/ojson"
)

// FaultHistory fails the change-log append after the transaction completed.
const FaultHistory = "history"

// Append is a hash-chained JSONL append that follows a committed
// transaction (the change log). The log is advisory: a failed or lost
// append leaves a gap readers detect, never a wrong entry.
type Append struct {
	Rel     string
	Payload []byte // a compact JSON object; seq and prev are prepended
	// ArchiveRel, when set, is where the log moves before the append if
	// it is at least RotateAt bytes by then. It is an intent resource.
	RotateAt   int64
	ArchiveRel string
}

// LineHash is the chain digest of one log line (without its newline).
func LineHash(line []byte) string {
	s := sha256.Sum256(line)
	return hex.EncodeToString(s[:])
}

// appendChained writes {"seq":n,"prev":h,...payload} as one line. A torn
// last line (no trailing newline) is cut first.
func appendChained(root string, a *Append) error {
	p := abs(root, a.Rel)
	if st, err := os.Lstat(p); err == nil && !st.Mode().IsRegular() {
		return fmt.Errorf("change log is not a regular file: %s", p)
	}
	last, size, err := lastLine(p)
	if err != nil {
		return err
	}
	seq, prev := int64(1), ""
	if last != nil {
		prev = LineHash(last)
		if v, perr := ojson.Parse(last); perr == nil {
			if n, ok := v.Value.Get("seq"); ok {
				if i, ierr := strconv.ParseInt(n.NumberLiteral(), 10, 64); ierr == nil {
					seq = i + 1
				}
			}
		}
	}
	if a.ArchiveRel != "" && size >= a.RotateAt {
		dst := abs(root, a.ArchiveRel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if _, err := os.Lstat(dst); err == nil {
			return fmt.Errorf("change log archive exists: %s", dst)
		}
		if err := os.Rename(p, dst); err != nil {
			return err
		}
		fsyncDir(filepath.Dir(dst))
	}
	if len(a.Payload) < 2 || a.Payload[0] != '{' {
		return errors.New("change log payload is not a JSON object")
	}
	var line bytes.Buffer
	line.WriteString(`{"seq":` + strconv.FormatInt(seq, 10) + `,"prev":`)
	if prev == "" {
		line.WriteString("null")
	} else {
		line.WriteString(`"` + prev + `"`)
	}
	if len(a.Payload) > 2 {
		line.WriteByte(',')
	}
	line.Write(a.Payload[1:])
	line.WriteByte('\n')

	_, statErr := os.Lstat(p)
	created := errors.Is(statErr, fs.ErrNotExist)
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(line.Bytes()); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if created {
		fsyncDir(filepath.Dir(p))
	}
	return nil
}

// lastLine returns the last complete line of p (nil when there is none)
// and the file size after cutting a torn tail.
func lastLine(p string) ([]byte, int64, error) {
	f, err := os.OpenFile(p, os.O_RDWR, 0)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, 0, nil
		}
		return nil, 0, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, 0, err
	}
	size := st.Size()
	const chunk = 64 << 10
	var tail []byte
	for off := size; off > 0; {
		n := int64(chunk)
		if off < n {
			n = off
		}
		off -= n
		buf := make([]byte, n)
		if _, err := f.ReadAt(buf, off); err != nil && err != io.EOF {
			return nil, 0, err
		}
		tail = append(buf, tail...)
		// Need the newline ending the previous line too.
		if bytes.Count(tail, []byte{'\n'}) >= 2 || off == 0 {
			break
		}
	}
	if len(tail) > 0 && tail[len(tail)-1] != '\n' {
		cut := bytes.LastIndexByte(tail, '\n') + 1
		size -= int64(len(tail) - cut)
		if err := f.Truncate(size); err != nil {
			return nil, 0, err
		}
		tail = tail[:cut]
	}
	if len(tail) == 0 {
		return nil, size, nil
	}
	body := tail[:len(tail)-1]
	return body[bytes.LastIndexByte(body, '\n')+1:], size, nil
}
