package engine

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/hoshinoht/shiori/internal/model"
	"github.com/hoshinoht/shiori/internal/ojson"
	"github.com/hoshinoht/shiori/internal/snapshot"
)

// Repair of unparseable plans:
//
//   - doctor recovers the old planFile link from the raw bytes with a
//     tolerant scan (recoveredPlanFile) when it is found, unambiguous and
//     passes the plan-file policy;
//   - create overwrite=true over an unreadable plan keeps using that
//     Markdown path unless planFile is given, and archives the exact
//     damaged bytes under archive/<id>/ in the same transaction;
//   - update and the other writers refuse an unreadable plan with a
//     message that names the repair and the doctor stateHash.

// planFileMember matches a "planFile": "<string>" member anywhere in the
// raw bytes, including a truncated or otherwise unparseable document.
var planFileMember = regexp.MustCompile(`"planFile"\s*:\s*("(?:[^"\\\x00-\x1f]|\\.)*")`)

// recoverPlanFile scans the raw bytes of an unreadable plan for its
// planFile link. It returns "" when no member is found, when members
// disagree, or when the value fails the plan-file policy (inside the root,
// under .opencode/workplan/, Markdown, no backslash, not a symlink) or is
// the linked Markdown of another readable plan.
func (e *Engine) recoverPlanFile(id string, raw []byte) string {
	found := ""
	for _, m := range planFileMember.FindAllSubmatch(raw, 8) {
		parsed, err := ojson.Parse(m[1])
		if err != nil || parsed.Value.Kind() != ojson.String {
			continue
		}
		v := model.TrimJS(parsed.Value.Str())
		if v == "" {
			continue
		}
		if found != "" && found != v {
			return "" // ambiguous
		}
		found = v
	}
	if found == "" || strings.Contains(found, `\`) {
		return ""
	}
	rel, err := snapshot.NormalizePlanFile(e.Root, found)
	if err != nil || snapshot.CheckWritable(e.Root, rel) != nil {
		return ""
	}
	if l, err := e.scanDir(); err == nil {
		var others []string
		for _, n := range l.primary {
			if n != id {
				others = append(others, n)
			}
		}
		if linked, _ := e.linkedMarkdown(others); linked[rel] {
			return "" // owned by another plan
		}
	}
	return rel
}

// repairArchive is the archive of an unreadable plan that
// create overwrite=true replaces: the exact damaged primary bytes (as a
// string when they are valid UTF-8, else base64) plus the linked Markdown
// and sidecars as they were. Same layout as the wipe and compaction
// archives (archiveVersion 1, archive/<id>/state-<stateHash:12>-<x:12>.json).
func (e *Engine) repairArchive(id string, u *snapshot.Unreadable, planFile string, md snapshot.Artifact, now string) (string, []byte) {
	sum := sha256.Sum256(u.JSON.Bytes)
	rawSHA := hex.EncodeToString(sum[:])
	rel := snapshot.WorkplanDir + "/archive/" + id + "/state-" + u.StateHash[:12] + "-" + rawSHA[:12] + ".json"
	src := ojson.NewObject(11).
		Set("workplanJsonPath", ojson.StringValue(u.JSON.Rel)).
		Set("workplanJsonSha256", ojson.StringValue(rawSHA))
	if utf8.Valid(u.JSON.Bytes) {
		src.Set("workplanJson", ojson.StringValue(string(u.JSON.Bytes)))
	} else {
		src.Set("workplanJson", ojson.NullValue()).
			Set("workplanJsonBase64", ojson.StringValue(base64.StdEncoding.EncodeToString(u.JSON.Bytes)))
	}
	src.Set("linkedMarkdownPath", ojson.StringValue(planFile)).
		Set("linkedMarkdown", artifactString(md)).
		Set("checkpointPath", ojson.StringValue(u.Checkpoint.Rel)).
		Set("checkpoint", artifactString(u.Checkpoint)).
		Set("dependencyPath", ojson.StringValue(u.Dependencies.Rel)).
		Set("dependencies", artifactString(u.Dependencies))
	archive := ojson.NewObject(7).
		Set("archiveVersion", ojson.IntValue(1)).
		Set("workplanId", ojson.StringValue(id)).
		Set("archivedAt", ojson.StringValue(now)).
		Set("reason", ojson.StringValue("workplan_create overwrite=true of an unreadable plan")).
		Set("operation", ojson.StringValue("create:overwrite")).
		Set("stateHash", ojson.StringValue(u.StateHash)).
		Set("source", src.Value()).Value()
	return rel, append(ojson.Pretty(archive), '\n')
}

// UnreadablePlanError is a writer's refusal of a plan whose primary JSON
// cannot be loaded; it names the explicit repair. The load error
// stays the cause, so the error class is unchanged.
type UnreadablePlanError struct {
	Err       error
	StateHash string
}

func (e *UnreadablePlanError) Error() string {
	return strings.TrimSuffix(e.Err.Error(), ".") + ". The plan cannot be loaded, so it cannot be changed in place. Repair it with workplan_create overwrite=true and expectedHash=" +
		e.StateHash + " (the stateHash workplan_doctor reports for it; the damaged bytes are archived first, and the linked Markdown is kept unless replaceMarkdown=true)."
}

func (e *UnreadablePlanError) Unwrap() error { return e.Err }

// repairHint wraps a writer's load error for an unreadable plan.
func (e *Engine) repairHint(id string, loadErr error) error {
	var already *UnreadablePlanError
	if errors.As(loadErr, &already) {
		return loadErr
	}
	u := e.unreadable(id, loadErr)
	if u == nil || u.Journal.Exists {
		return loadErr
	}
	return &UnreadablePlanError{Err: loadErr, StateHash: u.StateHash}
}
