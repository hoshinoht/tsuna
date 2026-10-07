package engine

import (
	"fmt"

	"github.com/hoshinoht/shiori/internal/input"
	"github.com/hoshinoht/shiori/internal/ojson"
)

// RunRead validates native-surface input and runs a read-only tool
// (list, read, inspect, validate, resume, doctor), returning the exact
// tool text and its value. runtimeFacts are the trusted host's doctor facts.
func (e *Engine) RunRead(tool string, raw ojson.Value, runtimeFacts *ojson.Value) (string, ojson.Value, error) {
	s := input.SurfaceNative
	var v ojson.Value
	var err error
	switch tool {
	case "list":
		in, perr := input.ParseListInput(raw, s)
		if perr != nil {
			return "", v, perr
		}
		v, err = e.List(in)
	case "read":
		in, perr := input.ParseReadInput(raw, s)
		if perr != nil {
			return "", v, perr
		}
		v, err = e.Read(in)
	case "inspect":
		in, perr := input.ParseInspectInput(raw, s)
		if perr != nil {
			return "", v, perr
		}
		v, err = e.Inspect(in)
	case "validate":
		in, perr := input.ParseValidateInput(raw, s)
		if perr != nil {
			return "", v, perr
		}
		v, err = e.Validate(in)
	case "resume":
		in, perr := input.ParseResumeInput(raw, s)
		if perr != nil {
			return "", v, perr
		}
		var text string
		v, text, err = e.Resume(in)
		if err != nil {
			return "", v, err
		}
		if text == "" {
			text = string(ojson.Pretty(v))
		}
		return text, v, nil
	case "doctor":
		in, perr := input.ParseDoctorInput(raw, s)
		if perr != nil {
			return "", v, perr
		}
		in.RuntimeFacts = runtimeFacts
		v, err = e.Doctor(in)
	default:
		return "", v, fmt.Errorf("not a read tool: %s", tool)
	}
	if err != nil {
		return "", v, err
	}
	return string(ojson.Pretty(v)), v, nil
}
