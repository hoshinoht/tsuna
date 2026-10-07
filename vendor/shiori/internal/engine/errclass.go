package engine

import (
	"errors"
	"strings"

	"github.com/hoshinoht/shiori/internal/input"
	"github.com/hoshinoht/shiori/internal/model"
	"github.com/hoshinoht/shiori/internal/snapshot"
	"github.com/hoshinoht/shiori/internal/storage"
)

// ErrorClass maps an operation error to a protocol error class
// (protocol-envelope-v1 errorClass). The CLI (--json) and the stdio
// protocol share this mapping.
func ErrorClass(err error) string {
	if c, ok := mutationErrorClass(err); ok {
		return c
	}
	var ie *input.InputError
	var de *model.DecodeError
	var nf *snapshot.NotFoundError
	var ij *snapshot.InvalidJSONError
	msg := err.Error()
	switch {
	case errors.As(err, &ie):
		return "invalid_input"
	case errors.Is(err, snapshot.ErrUnsupported):
		return "unsupported_capability"
	case errors.As(err, &nf):
		return "missing_artifact"
	case errors.As(err, &de), errors.As(err, &ij), strings.Contains(msg, "Migrate ids before"):
		return "invalid_structure"
	case strings.HasPrefix(msg, "Stale or option-mismatched"):
		return "stale_state"
	case strings.HasPrefix(msg, "Invalid workplan") && strings.HasSuffix(msg, "restart without a cursor"),
		strings.HasPrefix(msg, "Phase not found"), strings.HasPrefix(msg, "Step filter"),
		errors.Is(err, model.ErrUnnormalizableID),
		strings.HasPrefix(msg, "Plan file must"), strings.HasPrefix(msg, "Spec file must"):
		return "invalid_input"
	}
	return "internal"
}

// mutationErrorClass maps mutation errors to protocol error classes.
func mutationErrorClass(err error) (string, bool) {
	var stale *StaleHashError
	var sstale *storage.StaleError
	var lock *storage.LockUnavailableError
	var repl *storage.LockReplacedError
	var rec *storage.RecoveryRequiredError
	var ext *ExternalEditError
	var canc *storage.CancelledError
	var dup *DuplicateMembersError
	var d7 *D7Error
	var jinv *JournalInvalidError
	var gate *StatusGateError
	var stepGate *StepStatusGateError
	var specMissing *SpecFileMissingError
	var noteSize *NoteTooLargeError
	msg := err.Error()
	switch {
	case errors.As(err, &stale), errors.As(err, &sstale):
		if strings.Contains(msg, "requires explicit recovery") {
			return "recovery_required", true
		}
		if strings.HasPrefix(msg, "Refusing to overwrite") {
			return "ownership_conflict", true
		}
		return "stale_state", true
	case errors.As(err, &lock), errors.As(err, &repl):
		return "lock_unavailable", true
	case errors.As(err, &ext):
		return "external_edit_conflict", true
	case errors.As(err, &rec):
		return "recovery_required", true
	case errors.As(err, &canc):
		return "cancelled", true
	case errors.Is(err, ErrDenied):
		return "permission_denied", true
	case errors.As(err, &specMissing), errors.As(err, &noteSize):
		return "invalid_input", true
	case errors.As(err, &dup), errors.As(err, &d7), errors.As(err, &jinv), errors.As(err, &gate), errors.As(err, &stepGate):
		return "invalid_structure", true
	case strings.HasPrefix(msg, "Workplan transaction pending requires explicit recovery"):
		return "recovery_required", true
	case strings.HasPrefix(msg, "Refusing to overwrite"), strings.HasPrefix(msg, "Workplan already exists"),
		strings.HasPrefix(msg, "Plan file already exists"), strings.HasPrefix(msg, "Plan file destination is already owned"),
		strings.HasPrefix(msg, "Workplan destination is claimed"), strings.HasPrefix(msg, "Ambiguous pending workplan"):
		return "ownership_conflict", true
	case strings.HasPrefix(msg, "Invalid dependency metadata"):
		// Every dependency refusal (writes, phase replacement, compaction
		// of an invalid sidecar) is a structure error; the message text is
		// unchanged.
		return "invalid_structure", true
	case strings.HasPrefix(msg, "Refusing to replace handwritten"):
		return "invalid_input", true
	case strings.HasPrefix(msg, "Cannot move workplan link"), strings.HasPrefix(msg, "Cannot overwrite missing"),
		strings.HasPrefix(msg, "No pending transaction"), strings.HasPrefix(msg, "Plan file not found"):
		return "missing_artifact", true
	}
	return "", false
}
