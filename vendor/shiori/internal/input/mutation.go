package input

import (
	"strconv"
	"strings"

	"github.com/hoshinoht/shiori/internal/advisor"
	"github.com/hoshinoht/shiori/internal/model"
	"github.com/hoshinoht/shiori/internal/ojson"
)

// Strict, schema-driven input parsing for the mutating tools
// (schema/v1/tools/*.input.schema.json). The parser reproduces the
// reference validator's accepted data (schema order, defaults inserted in
// place) and its issue text and paths. Unknown keys are rejected at every
// level, including nested objects (the reference strips nested ones).

type fkind int

const (
	kString fkind = iota
	kBool
	kEnum
	kHash
	kID
	kNonblank
	kStringList
	kIntList
	kObject
	kObjectList
	kInt          // integer within [min, max] (noteRollover.keepLatest)
	kStringOrList // a string or an array of strings (appendValidation)
)

type fspec struct {
	key      string
	kind     fkind
	required bool
	enum     []string
	obj      *ospec
	def      *ojson.Value
	min, max int64 // kInt bounds
}

type ospec struct{ fields []fspec }

func str(key string) fspec     { return fspec{key: key, kind: kString} }
func reqStr(key string) fspec  { return fspec{key: key, kind: kString, required: true} }
func boolean(key string) fspec { return fspec{key: key, kind: kBool} }
func enum(key string, e []string) fspec {
	return fspec{key: key, kind: kEnum, enum: e}
}
func hash(key string) fspec              { return fspec{key: key, kind: kHash} }
func strList(key string) fspec           { return fspec{key: key, kind: kStringList} }
func intList(key string) fspec           { return fspec{key: key, kind: kIntList} }
func objList(key string, o *ospec) fspec { return fspec{key: key, kind: kObjectList, obj: o} }
func withDefault(f fspec, v ojson.Value) fspec {
	f.def = &v
	return f
}
func required(f fspec) fspec { f.required = true; return f }
func intRange(key string, min, max int64) fspec {
	return fspec{key: key, kind: kInt, min: min, max: max}
}

var statusEnum = model.Statuses

var (
	stepSpec        = &ospec{fields: []fspec{str("id"), reqStr("title"), str("target"), str("action"), str("validation"), enum("status", statusEnum)}}
	phaseSpec       = &ospec{fields: []fspec{str("id"), reqStr("title"), enum("status", statusEnum), objList("steps", stepSpec)}}
	findingSpec     = &ospec{fields: []fspec{required(enum("severity", model.Severities)), reqStr("title"), str("detail"), str("source"), enum("status", model.FindingStatuses)}}
	refSpec         = &ospec{fields: []fspec{{key: "phaseId", kind: kNonblank, required: true}, {key: "stepId", kind: kNonblank, required: true}}}
	depSpec         = &ospec{fields: []fspec{{key: "phaseId", kind: kNonblank, required: true}, {key: "stepId", kind: kNonblank, required: true}, required(objList("dependsOn", refSpec))}}
	updatePhaseSpec = &ospec{fields: []fspec{reqStr("phaseId"), str("title"), enum("status", statusEnum)}}
	addPhaseSpec    = &ospec{fields: []fspec{str("afterPhaseId"), {key: "phase", kind: kObject, required: true, obj: phaseSpec}}}
	updateStepSpec  = &ospec{fields: []fspec{reqStr("phaseId"), reqStr("stepId"), str("title"), str("target"), str("action"), str("validation"), enum("status", statusEnum)}}
	addStepSpec     = &ospec{fields: []fspec{reqStr("phaseId"), str("afterStepId"), {key: "step", kind: kObject, required: true, obj: stepSpec}}}
	// One evidence record (spec 06 X2).
	evidenceSpec = &ospec{fields: []fspec{
		{key: "phaseId", kind: kNonblank, required: true}, {key: "stepId", kind: kNonblank, required: true},
		{key: "command", kind: kNonblank, required: true},
		required(intRange("exitCode", -2147483648, 2147483647)),
		str("output"), hash("outputDigest"), str("summary"), strList("scope"), str("lane"),
	}}
	// One lanes operation (spec 06 X3).
	laneOpSpec = &ospec{fields: []fspec{
		required(enum("op", []string{"propose", "transition", "claims"})),
		{key: "laneId", kind: kNonblank, required: true},
		objList("steps", refSpec), strList("claims"),
		enum("state", []string{"claimed", "prepared", "running", "review", "integrating", "merged", "abandoned"}),
		{key: "checkout", kind: kObject, obj: &ospec{fields: []fspec{{key: "path", kind: kNonblank, required: true}, str("branch")}}},
		strList("add"), strList("remove"),
	}}
	planLinkSpec = &ospec{fields: []fspec{
		{key: "planId", kind: kNonblank, required: true},
		required(enum("relation", []string{"blocks", "blockedBy", "related"})),
		str("note"),
	}}
	// The note rollover selector.
	rolloverSpec = &ospec{fields: []fspec{intRange("keepLatest", 1, advisor.MaxRolloverKeep), intList("pinNoteIndexes")}}
)

var toolSpecs = map[string]*ospec{
	"create": {fields: []fspec{
		{key: "id", kind: kID, required: true},
		withDefault(str("kind"), ojson.StringValue("general")),
		str("title"), reqStr("goal"),
		strList("scope"), strList("nonGoals"), strList("constraints"), strList("relevantFiles"),
		str("planFile"), str("planMarkdown"), strList("specFiles"),
		objList("phases", phaseSpec), objList("reviewFindings", findingSpec), strList("notes"),
		withDefault(enum("status", statusEnum), ojson.StringValue("draft")),
		withDefault(boolean("overwrite"), ojson.BoolValue(false)),
		hash("expectedHash"), boolean("replaceMarkdown"),
	}},
	"update": {fields: []fspec{
		{key: "id", kind: kID, required: true},
		hash("expectedHash"), boolean("rebase"), enum("recovery", []string{"resume", "rollback"}), boolean("replaceMarkdown"),
		str("title"), str("goal"), enum("status", statusEnum),
		strList("scope"), strList("nonGoals"), strList("constraints"),
		str("planFile"), str("planMarkdown"), strList("specFiles"),
		objList("reviewFindings", findingSpec), objList("phases", phaseSpec),
		objList("updatePhases", updatePhaseSpec), objList("addPhases", addPhaseSpec),
		objList("updateSteps", updateStepSpec), objList("addSteps", addStepSpec),
		strList("addRelevantFiles"), strList("addSpecFiles"), strList("removeSpecFiles"),
		objList("addReviewFindings", findingSpec), strList("appendNotes"),
		objList("dependencies", depSpec),
		objList("recordEvidence", evidenceSpec),
		objList("lanes", laneOpSpec),
		objList("planLinks", planLinkSpec),
	}},
	"patch": {fields: []fspec{{key: "id", kind: kID, required: true}, reqStr("patchText"), boolean("validate"), hash("expectedHash")}},
	"reset": {fields: []fspec{
		{key: "id", kind: kID, required: true},
		withDefault(enum("mode", []string{"draft", "markdown-only", "wipe"}), ojson.StringValue("draft")),
		withDefault(boolean("preserveNotes"), ojson.BoolValue(false)),
		boolean("replaceMarkdown"), hash("expectedHash"),
		str("previewToken"), str("confirmation"), // mode=wipe apply
	}},
	"checkpoint": {fields: []fspec{
		{key: "id", kind: kID, required: true}, reqStr("summary"), reqStr("nextAction"),
		str("phaseId"), str("stepId"),
		strList("blockers"), strList("recentValidation"), strList("guardrails"), strList("references"),
		hash("expectedHash"),
		boolean("merge"), {key: "appendValidation", kind: kStringOrList},
	}},
	"compact": {fields: []fspec{
		{key: "id", kind: kID, required: true},
		withDefault(enum("mode", []string{"preview", "apply"}), ojson.StringValue("preview")),
		reqStr("archiveReason"), strList("completedPhaseIds"), intList("noteIndexes"), intList("resolvedFindingIndexes"),
		str("confirmation"), str("previewToken"), hash("expectedHash"),
		{key: "noteRollover", kind: kObject, obj: rolloverSpec},
	}},
	"compact_preview": {fields: []fspec{
		{key: "id", kind: kID, required: true},
		reqStr("archiveReason"), strList("completedPhaseIds"), intList("noteIndexes"), intList("resolvedFindingIndexes"),
		str("confirmation"), str("previewToken"), hash("expectedHash"),
		{key: "noteRollover", kind: kObject, obj: rolloverSpec},
	}},
}

// checkpointMergeSpec is the checkpoint spec with merge=true: summary and
// nextAction may be omitted and then
// keep the stored checkpoint's values.
var checkpointMergeSpec = func() *ospec {
	fields := append([]fspec(nil), toolSpecs["checkpoint"].fields...)
	for i := range fields {
		if fields[i].key == "summary" || fields[i].key == "nextAction" {
			fields[i].required = false
		}
	}
	return &ospec{fields: fields}
}()

type issueList struct{ issues []model.Issue }

func (l *issueList) add(path []string, msg string) {
	p := append([]string(nil), path...)
	l.issues = append(l.issues, model.Issue{Path: p, Message: msg})
}

func pathWith(path []string, seg string) []string {
	out := make([]string, len(path)+1)
	copy(out, path)
	out[len(path)] = seg
	return out
}

// parseObject validates v against spec and returns the accepted data.
func parseObject(spec *ospec, v ojson.Value, path []string, l *issueList, extraKnown map[string]bool, prefix []ojson.Member) (ojson.Value, bool) {
	if v.Kind() != ojson.Object {
		l.add(path, "Invalid input: expected object, received "+v.TypeName())
		return ojson.Value{}, false
	}
	members := map[string]ojson.Value{}
	var order []string
	for _, m := range v.UniqueMembers() {
		members[m.Key] = m.Value
		order = append(order, m.Key)
	}
	start := len(l.issues)
	out := append([]ojson.Member(nil), prefix...)
	known := map[string]bool{}
	for k := range extraKnown {
		known[k] = true
	}
	for _, f := range spec.fields {
		known[f.key] = true
		fv, present := members[f.key]
		fp := pathWith(path, f.key)
		if !present {
			if f.def != nil {
				out = append(out, ojson.Member{Key: f.key, Value: *f.def})
			} else if f.required {
				l.add(fp, "Invalid input: expected "+expectedType(f.kind)+", received undefined")
			}
			continue
		}
		if val, ok := parseField(f, fv, fp, l); ok {
			out = append(out, ojson.Member{Key: f.key, Value: val})
		}
	}
	var unknown []string
	for _, k := range order {
		if !known[k] {
			unknown = append(unknown, strconv.Quote(k))
		}
	}
	switch len(unknown) {
	case 0:
	case 1:
		l.add(path, "Unrecognized key: "+unknown[0])
	default:
		l.add(path, "Unrecognized keys: "+strings.Join(unknown, ", "))
	}
	return ojson.ObjectValue(out), len(l.issues) == start
}

func expectedType(k fkind) string {
	switch k {
	case kBool:
		return "boolean"
	case kStringList, kIntList, kObjectList:
		return "array"
	case kObject:
		return "object"
	case kInt:
		return "number"
	case kStringOrList:
		return "string or array"
	default:
		return "string"
	}
}

func parseField(f fspec, v ojson.Value, path []string, l *issueList) (ojson.Value, bool) {
	typeErr := func(t string) (ojson.Value, bool) {
		l.add(path, "Invalid input: expected "+t+", received "+v.TypeName())
		return ojson.Value{}, false
	}
	switch f.kind {
	case kString:
		if v.Kind() != ojson.String {
			return typeErr("string")
		}
		return v, true
	case kBool:
		if v.Kind() != ojson.Bool {
			return typeErr("boolean")
		}
		return v, true
	case kEnum:
		if v.Kind() == ojson.String {
			for _, e := range f.enum {
				if e == v.Str() {
					return v, true
				}
			}
		}
		q := make([]string, len(f.enum))
		for i, e := range f.enum {
			q[i] = strconv.Quote(e)
		}
		l.add(path, "Invalid option: expected one of "+strings.Join(q, "|"))
		return ojson.Value{}, false
	case kHash:
		if v.Kind() != ojson.String {
			return typeErr("string")
		}
		if !hashRE.MatchString(v.Str()) {
			l.add(path, "Invalid string: must match pattern /^[a-f0-9]{64}$/")
			return ojson.Value{}, false
		}
		return v, true
	case kID:
		if v.Kind() != ojson.String {
			return typeErr("string")
		}
		s := model.TrimJS(v.Str())
		ok := true
		if s == "" {
			l.add(path, "Too small: expected string to have >=1 characters")
			ok = false
		}
		if !idPattern.MatchString(s) {
			l.add(path, "Invalid string: must match pattern /[a-z0-9]/i")
			ok = false
		}
		return ojson.StringValue(s), ok
	case kNonblank:
		if v.Kind() != ojson.String {
			return typeErr("string")
		}
		s := model.TrimJS(v.Str())
		if s == "" {
			l.add(path, "Too small: expected string to have >=1 characters")
			return ojson.Value{}, false
		}
		return ojson.StringValue(s), true
	case kStringList:
		if v.Kind() != ojson.Array {
			return typeErr("array")
		}
		ok := true
		for i, e := range v.Elems() {
			if e.Kind() != ojson.String {
				l.add(pathWith(path, strconv.Itoa(i)), "Invalid input: expected string, received "+e.TypeName())
				ok = false
			}
		}
		return v, ok
	case kIntList:
		if v.Kind() != ojson.Array {
			return typeErr("array")
		}
		ok := true
		out := make([]ojson.Value, 0, len(v.Elems()))
		for i, e := range v.Elems() {
			ep := pathWith(path, strconv.Itoa(i))
			if e.Kind() != ojson.Number {
				l.add(ep, "Invalid input: expected number, received "+e.TypeName())
				ok = false
				continue
			}
			fl, _ := e.Float()
			if fl != float64(int64(fl)) || fl > 9007199254740991 || fl < -9007199254740991 {
				l.add(ep, "Invalid input: expected int, received number")
				ok = false
				continue
			}
			if fl < 0 {
				l.add(ep, "Too small: expected number to be >=0")
				ok = false
				continue
			}
			out = append(out, ojson.IntValue(int64(fl)))
		}
		return ojson.ArrayValue(out), ok
	case kStringOrList:
		if v.Kind() == ojson.String {
			return v, true
		}
		if v.Kind() != ojson.Array {
			return typeErr("string or array")
		}
		return parseField(fspec{key: f.key, kind: kStringList}, v, path, l)
	case kObject:
		return parseObject(f.obj, v, path, l, nil, nil)
	case kInt:
		if v.Kind() != ojson.Number {
			return typeErr("number")
		}
		fl, _ := v.Float()
		if fl != float64(int64(fl)) || fl > 9007199254740991 || fl < -9007199254740991 {
			l.add(path, "Invalid input: expected int, received number")
			return ojson.Value{}, false
		}
		if int64(fl) < f.min {
			l.add(path, "Too small: expected number to be >="+strconv.FormatInt(f.min, 10))
			return ojson.Value{}, false
		}
		if int64(fl) > f.max {
			l.add(path, "Too big: expected number to be <="+strconv.FormatInt(f.max, 10))
			return ojson.Value{}, false
		}
		return ojson.IntValue(int64(fl)), true
	case kObjectList:
		if v.Kind() != ojson.Array {
			return typeErr("array")
		}
		ok := true
		out := make([]ojson.Value, 0, len(v.Elems()))
		for i, e := range v.Elems() {
			val, good := parseObject(f.obj, e, pathWith(path, strconv.Itoa(i)), l, nil, nil)
			if !good {
				ok = false
				continue
			}
			out = append(out, val)
		}
		return ojson.ArrayValue(out), ok
	}
	return ojson.Value{}, false
}

// ParseMutationInput validates the input of a mutating tool ("create",
// "update", "patch", "reset", "checkpoint", "compact", "compact_preview")
// on a surface and returns the accepted data object in schema order.
func ParseMutationInput(tool string, v ojson.Value, s Surface) (ojson.Value, error) {
	spec := toolSpecs[tool]
	if spec == nil {
		return ojson.Value{}, &InputError{Tool: tool, Issues: []model.Issue{{Message: "unknown tool"}}}
	}
	var l issueList
	var extra map[string]bool
	var prefix []ojson.Member
	if s == SurfaceCore {
		extra = map[string]bool{"workspaceRoot": true}
		if v.Kind() == ojson.Object {
			if wr, ok := v.Get("workspaceRoot"); ok {
				if wr.Kind() != ojson.String {
					l.add([]string{"workspaceRoot"}, "Invalid input: expected string, received "+wr.TypeName())
				} else {
					prefix = []ojson.Member{{Key: "workspaceRoot", Value: wr}}
				}
			}
		}
	}
	if tool == "checkpoint" && v.Kind() == ojson.Object {
		if mg, ok := v.Get("merge"); ok && mg.Kind() == ojson.Bool && mg.Bool() {
			spec = checkpointMergeSpec
		}
	}
	data, ok := parseObject(spec, v, nil, &l, extra, prefix)
	if ok && len(l.issues) == 0 {
		refine(tool, data, v, s, &l)
	}
	if len(l.issues) > 0 {
		return ojson.Value{}, &InputError{Tool: tool, Issues: l.issues}
	}
	return data, nil
}

// nativeHashGuidance is appended to the native missing-expectedHash
// refusal: it names where the hash comes from
// without echoing the current one, so an agent re-reads before retrying.
const nativeHashGuidance = " — pass expectedHash set to the stateHash from your last successful write, or re-read with workplan_resume or workplan_inspect first"

const (
	msgNativeHash       = "Native existing-state writes require the current stateHash" + nativeHashGuidance
	msgCoreOverwrite    = "overwrite requires the current stateHash"
	msgRecoveryExcl     = "recovery is mutually exclusive with ordinary update fields"
	msgApplyTokenNative = "Apply requires the matching preview token"
	msgApplyTokenCore   = "apply requires the token from the matching preview"
)

// Confirmation phrases and the refusals the engine repeats under the lock.
const (
	// ConfirmArchive is the compaction apply confirmation phrase.
	ConfirmArchive  = "ARCHIVE_SELECTED_HISTORY"
	MsgApplyConfirm = "Apply requires confirmation=ARCHIVE_SELECTED_HISTORY"
	// MsgRolloverWithIndexes refuses a compaction that names notes twice.
	MsgRolloverWithIndexes = "noteRollover cannot be combined with noteIndexes; noteRollover selects the notes"

	// ConfirmWipe is the wipe apply confirmation phrase.
	ConfirmWipe        = "WIPE_PLAN_CONTENT"
	MsgWipeConfirm     = "Wipe apply requires confirmation=" + ConfirmWipe
	MsgWipeToken       = "Wipe apply requires the previewToken from a workplan_reset mode=wipe preview (call it without previewToken and confirmation first)"
	MsgWipeOnlyOptions = "previewToken and confirmation apply only to mode=wipe"
)

// refine applies the cross-field rules (x-shiori-rules) after the shape
// checks pass.
func refine(tool string, data, raw ojson.Value, s Surface, l *issueList) {
	has := func(k string) bool { _, ok := data.Get(k); return ok }
	native := s == SurfaceNative
	switch tool {
	case "create":
		if ov, _ := data.Get("overwrite"); ov.Bool() && !has("expectedHash") {
			if native {
				l.add([]string{"expectedHash"}, msgNativeHash)
			} else {
				l.add([]string{"expectedHash"}, msgCoreOverwrite)
			}
		}
	case "update":
		if native && !has("expectedHash") {
			l.add([]string{"expectedHash"}, msgNativeHash)
		}
		if has("recovery") {
			for _, m := range data.Members() {
				switch m.Key {
				case "id", "expectedHash", "recovery":
					continue
				case "workspaceRoot":
					if !native {
						continue
					}
				case "replaceMarkdown":
					if !native && !m.Value.Bool() {
						continue
					}
				}
				l.add([]string{"recovery"}, msgRecoveryExcl)
				break
			}
		}
		refineEvidence(data, l)
		refineLanes(data, l)
		if lv, ok := data.Get("planLinks"); ok && len(lv.Elems()) > MaxPlanLinks {
			l.add([]string{"planLinks"}, "Too big: expected array to have <="+strconv.Itoa(MaxPlanLinks)+" items")
		}
	case "patch", "reset", "checkpoint":
		if native && !has("expectedHash") {
			l.add([]string{"expectedHash"}, msgNativeHash)
		}
		if tool == "checkpoint" {
			// A withheld resume value copied back as text.
			for _, k := range []string{"summary", "nextAction"} {
				if x, ok := data.Get(k); ok && withheldPlaceholder(x.Str()) {
					l.add([]string{k}, msgWithheldPlaceholder(k))
				}
			}
		}
		if tool == "reset" {
			// The wipe apply fields.
			mode, _ := data.Get("mode")
			for _, k := range []string{"previewToken", "confirmation"} {
				if has(k) && mode.Str() != "wipe" {
					l.add([]string{k}, MsgWipeOnlyOptions)
				}
			}
			if mode.Str() == "wipe" && (has("previewToken") || has("confirmation")) {
				if c, _ := data.Get("confirmation"); c.Str() != ConfirmWipe {
					l.add([]string{"confirmation"}, MsgWipeConfirm)
				}
				if !has("previewToken") {
					l.add([]string{"previewToken"}, MsgWipeToken)
				}
			}
		}
	case "compact", "compact_preview":
		// noteRollover selects the notes itself.
		if has("noteRollover") && has("noteIndexes") {
			l.add([]string{"noteRollover"}, MsgRolloverWithIndexes)
		}
		if mode, _ := data.Get("mode"); mode.Str() == "apply" {
			if native {
				if !has("expectedHash") {
					l.add([]string{"expectedHash"}, msgNativeHash)
				}
				if c, _ := data.Get("confirmation"); c.Str() != ConfirmArchive {
					l.add([]string{"confirmation"}, MsgApplyConfirm)
				}
				if !has("previewToken") {
					l.add([]string{"previewToken"}, msgApplyTokenNative)
				}
			} else if !has("previewToken") {
				l.add([]string{"previewToken"}, msgApplyTokenCore)
			}
		}
	}
}

// withheldPlaceholder reports whether a checkpoint summary or nextAction
// is the text of a withheld resume value:
// trimmed, exactly "null" or "undefined", or starting with "null " or
// "undefined " (case-sensitive).
func withheldPlaceholder(s string) bool {
	t := model.TrimJS(s)
	return t == "null" || t == "undefined" || strings.HasPrefix(t, "null ") || strings.HasPrefix(t, "undefined ")
}

// msgWithheldPlaceholder explains the withheld-placeholder refusal. It
// contains no "; " so the issue list stays separable.
func msgWithheldPlaceholder(field string) string {
	return "Checkpoint " + field + " starts with null/undefined, which is how workplan_resume shows a withheld field of a stale or legacy checkpoint, not its value — read the stored checkpoint first (the file named in the resume instruction), or pass merge=true and omit " + field + " to keep the stored value"
}

// Evidence record limits (UTF-16 code units).
const (
	MaxEvidenceRecords = 20
	MaxEvidenceCommand = 2000
	MaxEvidenceSummary = 500
	MaxEvidenceScope   = 50
)

const msgEvidenceOutput = "output and outputDigest are mutually exclusive; pass the output text or its sha256"

// refineEvidence applies the recordEvidence size and exclusivity rules.
func refineEvidence(data ojson.Value, l *issueList) {
	rv, ok := data.Get("recordEvidence")
	if !ok {
		return
	}
	if n := len(rv.Elems()); n > MaxEvidenceRecords {
		l.add([]string{"recordEvidence"}, "Too big: expected array to have <="+strconv.Itoa(MaxEvidenceRecords)+" items")
	}
	for i, e := range rv.Elems() {
		at := []string{"recordEvidence", strconv.Itoa(i)}
		if c, _ := e.Get("command"); ojson.UTF16Len(c.Str()) > MaxEvidenceCommand {
			l.add(append(at, "command"), "Too big: expected string to have <="+strconv.Itoa(MaxEvidenceCommand)+" characters")
		}
		if sm, ok := e.Get("summary"); ok && ojson.UTF16Len(sm.Str()) > MaxEvidenceSummary {
			l.add(append(at, "summary"), "Too big: expected string to have <="+strconv.Itoa(MaxEvidenceSummary)+" characters")
		}
		if sc, ok := e.Get("scope"); ok && len(sc.Elems()) > MaxEvidenceScope {
			l.add(append(at, "scope"), "Too big: expected array to have <="+strconv.Itoa(MaxEvidenceScope)+" items")
		}
		_, out := e.Get("output")
		_, dig := e.Get("outputDigest")
		if out && dig {
			l.add(append(at, "output"), msgEvidenceOutput)
		}
	}
}

// MaxLaneOps bounds the lanes operations of one update.
const MaxLaneOps = 20

// MaxPlanLinks bounds workplan_update.planLinks.
const MaxPlanLinks = 100

// laneOpFields are the members each lanes operation takes besides op and
// laneId (required first).
var laneOpFields = map[string][]string{
	"propose":    {"steps", "claims"},
	"transition": {"state", "checkout"},
	"claims":     {"add", "remove"},
}

// refineLanes checks each operation names only its own members.
func refineLanes(data ojson.Value, l *issueList) {
	rv, ok := data.Get("lanes")
	if !ok {
		return
	}
	if len(rv.Elems()) > MaxLaneOps {
		l.add([]string{"lanes"}, "Too big: expected array to have <="+strconv.Itoa(MaxLaneOps)+" items")
	}
	for i, e := range rv.Elems() {
		at := []string{"lanes", strconv.Itoa(i)}
		op, _ := e.Get("op")
		allowed := laneOpFields[op.Str()]
		for _, m := range e.Members() {
			if m.Key == "op" || m.Key == "laneId" {
				continue
			}
			ok := false
			for _, a := range allowed {
				ok = ok || a == m.Key
			}
			if !ok {
				l.add(append(at, m.Key), "Not allowed for op "+op.Str())
			}
		}
		switch op.Str() {
		case "propose":
			if st, _ := e.Get("steps"); len(st.Elems()) == 0 {
				l.add(append(at, "steps"), "A proposed lane needs at least one step")
			}
		case "transition":
			if _, ok := e.Get("state"); !ok {
				l.add(append(at, "state"), "Invalid input: expected string, received undefined")
			}
		case "claims":
			_, a := e.Get("add")
			_, r := e.Get("remove")
			if !a && !r {
				l.add(at, "A claims operation needs add or remove")
			}
		}
	}
}
