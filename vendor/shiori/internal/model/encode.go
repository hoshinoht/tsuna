package model

import "github.com/hoshinoht/shiori/internal/ojson"

// Writers emit JSON.stringify(value, null, 2) + "\n" in the reference key
// order (schema x-shiori-write-key-order).

func refValue(r StepRef) ojson.Value {
	return ojson.NewObject(2).Set("phaseId", ojson.StringValue(r.PhaseID)).Set("stepId", ojson.StringValue(r.StepID)).Value()
}

// DependenciesValue renders dependencies-v1; terminalSummaries is present
// only when non-empty.
func DependenciesValue(d *Dependencies) ojson.Value {
	entries := make([]ojson.Value, len(d.Entries))
	for i, en := range d.Entries {
		on := make([]ojson.Value, len(en.DependsOn))
		for j, r := range en.DependsOn {
			on[j] = refValue(r)
		}
		entries[i] = ojson.NewObject(3).
			Set("phaseId", ojson.StringValue(en.PhaseID)).
			Set("stepId", ojson.StringValue(en.StepID)).
			Set("dependsOn", ojson.ArrayValue(on)).Value()
	}
	b := ojson.NewObject(5).
		Set("schemaVersion", ojson.IntValue(1)).
		Set("id", ojson.StringValue(d.ID)).
		Set("updatedAt", ojson.StringValue(d.UpdatedAt)).
		Set("dependencies", ojson.ArrayValue(entries))
	if len(d.TerminalSummaries) > 0 {
		ts := make([]ojson.Value, len(d.TerminalSummaries))
		for i, t := range d.TerminalSummaries {
			ts[i] = ojson.NewObject(4).
				Set("phaseId", ojson.StringValue(t.PhaseID)).
				Set("stepId", ojson.StringValue(t.StepID)).
				Set("title", ojson.StringValue(t.Title)).
				Set("status", ojson.StringValue(t.Status)).Value()
		}
		b.Set("terminalSummaries", ojson.ArrayValue(ts))
	}
	return b.Value()
}

// EncodeDependencies is the stored form of dependencies-v1.
func EncodeDependencies(d *Dependencies) []byte {
	return append(ojson.Pretty(DependenciesValue(d)), '\n')
}

// PositionValue renders a checkpoint position (or null).
func PositionValue(p *Position) ojson.Value {
	if p == nil {
		return ojson.NullValue()
	}
	return ojson.NewObject(6).
		Set("phaseId", ojson.StringValue(p.PhaseID)).
		Set("phaseTitle", ojson.StringValue(p.PhaseTitle)).
		Set("phaseStatus", ojson.StringValue(p.PhaseStatus)).
		Set("stepId", ojson.StringValue(p.StepID)).
		Set("stepTitle", ojson.StringValue(p.StepTitle)).
		Set("stepStatus", ojson.StringValue(p.StepStatus)).Value()
}

func manifestValue(m []ManifestEntry) ojson.Value {
	out := make([]ojson.Value, len(m))
	for i, e := range m {
		b := ojson.NewObject(2).Set("path", ojson.StringValue(e.Path))
		if e.Missing {
			b.Set("missing", ojson.BoolValue(true))
		} else {
			b.Set("sha256", ojson.StringValue(e.SHA256))
		}
		out[i] = b.Value()
	}
	return ojson.ArrayValue(out)
}

// CheckpointValue renders checkpoint v2 in the checkpoint tool's key
// order. With bindingLast (compaction refresh) the planHash/manifest/
// evidenceStatus members follow the carried-forward fields, as the
// reference compaction writer emits them.
func CheckpointValue(c *Checkpoint, bindingLast bool) ojson.Value {
	b := ojson.NewObject(16).
		Set("schemaVersion", ojson.IntValue(2)).
		Set("id", ojson.StringValue(c.ID)).
		Set("sourceUpdatedAt", ojson.StringValue(c.SourceUpdatedAt))
	binding := func() {
		b.Set("planHash", ojson.StringValue(c.PlanHash)).
			Set("manifest", manifestValue(c.Manifest)).
			Set("evidenceStatus", ojson.StringValue("unverified"))
	}
	if !bindingLast {
		binding()
	}
	b.Set("createdAt", ojson.StringValue(c.CreatedAt)).
		Set("updatedAt", ojson.StringValue(c.UpdatedAt)).
		Set("status", ojson.StringValue(c.Status)).
		Set("summary", ojson.StringValue(c.Summary)).
		Set("current", PositionValue(c.Current)).
		Set("nextAction", ojson.StringValue(c.NextAction)).
		Set("blockers", ojson.StringsValue(c.Blockers)).
		Set("recentValidation", ojson.StringsValue(c.RecentValidation)).
		Set("guardrails", ojson.StringsValue(c.Guardrails)).
		Set("references", ojson.StringsValue(c.References))
	if bindingLast {
		binding()
	}
	return b.Value()
}
