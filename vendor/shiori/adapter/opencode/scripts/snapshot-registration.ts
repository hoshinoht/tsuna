// Regenerates src/registration.json from the reference TypeScript workplan
// plugin so that the adapter registers byte-identical tool descriptions and
// input schemas (same identities, argument shapes and model-facing text),
// plus the approved D.1 addition (workplan_read includeNotes), the D.3
// reset changes (wipe mode, previewToken, confirmation, accurate text),
// the D.4 compaction noteRollover selector, the D.4.1 expectedHash
// sentence on the mutating tools' descriptions and the D.4.3 checkpoint
// merge mode.
//
//   bun scripts/snapshot-registration.ts /path/to/workplan-tools/src/core > src/registration.json
//
// The reference is only executed; nothing else is copied. Input
// validation itself stays in the Go core (schema/v1/tools).
const core = process.argv[2];
if (!core) {
  console.error("usage: bun scripts/snapshot-registration.ts <workplan-tools/src/core>");
  process.exit(2);
}
const mod = await import(core);
const { nativeWorkplanInputSchemas, workplanInputJsonSchema, workplanToolDefinitions, createNativeWorkplanInputSchema } = mod;
const order = ["create", "update", "inspect", "validate", "read", "list", "patch", "reset", "resume", "checkpoint", "compact", "doctor"];
const tools: Array<{ name: string; description: string; input: unknown }> = [];
for (const key of order) {
  tools.push({
    name: `workplan_${key}`,
    description: workplanToolDefinitions[`workplan_${key}`].description,
    input: workplanInputJsonSchema(nativeWorkplanInputSchemas[key]),
  });
}
const { mode: _mode, ...previewArgs } = workplanToolDefinitions.workplan_compact.args;
tools.push({
  name: "workplan_compact_preview",
  description: "Read-only preview of the exact workplan history selection and archive intent; this tool never applies compaction.",
  input: workplanInputJsonSchema(createNativeWorkplanInputSchema(previewArgs)),
});
const reference = JSON.stringify({ source: "reference workplan-tools native registration (generated; do not edit)", tools }, null, 2) + "\n";
// Approved design change D.1 (docs/contracts.md §11 item B, 2026-09-30):
// the only deliberate difference from the reference registration. The
// adapter test removes these additions and the "d1" key again and checks
// the result against referenceSha256.
const { createHash } = await import("node:crypto");
const read = tools.find((tool) => tool.name === "workplan_read")!.input as { properties: Record<string, unknown> };
read.properties.includeNotes = {
  description: "Only with phaseId/stepId: also return reviewFindings and notes (default false). A filtered read returns the plan header, hashes and the selected phase/step; linked Markdown only with includeMarkdown=true",
  type: "boolean",
};
// Approved design change D.3 (docs/contracts.md §13 item 1, 2026-09-30):
// the reset wipe mode with its preview token and confirmation, and
// reset text that describes the status-only draft reset. Each changed
// value is recorded with its reference value so the test can restore it.
const reset = tools.find((tool) => tool.name === "workplan_reset")! as { description: string; input: any };
const changes: Array<{ tool: string; path: string[]; reference: unknown }> = [];
const change = (path: string[], value: unknown) => {
  let cur: any = reset;
  for (const key of path.slice(0, -1)) cur = cur[key];
  changes.push({ tool: "workplan_reset", path, reference: cur[path[path.length - 1]] });
  cur[path[path.length - 1]] = value;
};
change(["description"], "Reset a workplan: draft resets every status (plan, phases, steps) to draft and removes the checkpoint, keeping phases, steps, notes, findings and dependencies; wipe clears phases, findings and notes after a preview and an exact confirmation, archiving the originals first; markdown-only only regenerates the linked Markdown from the JSON.");
change(["input", "properties", "mode", "description"], "draft resets statuses only and keeps the plan content; wipe clears phases, findings and (unless preserveNotes) notes: call it first without previewToken/confirmation for a read-only preview, then again with that previewToken and confirmation=WIPE_PLAN_CONTENT; markdown-only only regenerates the Markdown");
change(["input", "properties", "mode", "enum"], ["draft", "markdown-only", "wipe"]);
change(["input", "properties", "preserveNotes", "description"], "Keep notes during a wipe (a draft reset always keeps notes)");
reset.input.properties.previewToken = { description: "mode=wipe only: the previewToken returned by the wipe preview", type: "string" };
reset.input.properties.confirmation = { description: "mode=wipe only: must be WIPE_PLAN_CONTENT to apply the previewed wipe", type: "string" };
// Approved design change D.4 (docs/contracts.md §15, 2026-09-30): the
// note rollover selector of workplan_compact and workplan_compact_preview,
// inserted after resolvedFindingIndexes.
const rollover = {
  description: "Instead of noteIndexes: archive every note older than the latest keepLatest (default 20) except pinned ones: [pinned] in the text or pinNoteIndexes, decision records (decision/decided or USER), notes naming an open step as phaseId/stepId or quoting an open finding title, and the latest 3 compaction archive pointers (older ones roll over). Apply needs a fresh checkpoint; pass the same noteRollover to preview and apply",
  type: "object",
  properties: {
    keepLatest: { description: "Number of newest notes that always stay (default 20)", type: "integer", minimum: 1, maximum: 10000 },
    pinNoteIndexes: { description: "Zero-based indexes of further notes to keep", type: "array", items: { type: "integer", minimum: 0, maximum: 9007199254740991 } },
  },
  additionalProperties: false,
};
for (const name of ["workplan_compact", "workplan_compact_preview"]) {
  const input = tools.find((tool) => tool.name === name)!.input as { properties: Record<string, unknown> };
  const next: Record<string, unknown> = {};
  for (const [key, value] of Object.entries(input.properties)) {
    next[key] = value;
    if (key === "resolvedFindingIndexes") next.noteRollover = rollover;
  }
  input.properties = next;
}
// Approved design change D.4.1 (docs/contracts.md §16, 2026-10-01): one
// sentence about expectedHash on each mutating tool's description. Each
// change records the previous value (the reference text, or the D.3 text
// for workplan_reset) so the test can restore it before the d3 changes.
const hashSentence = "pass expectedHash = the stateHash from your latest read or successful write.";
const d41Sentences: Array<[string, string]> = [
  ["workplan_create", `With overwrite=true, ${hashSentence}`],
  ["workplan_update", `P${hashSentence.slice(1)}`],
  ["workplan_patch", `P${hashSentence.slice(1)}`],
  ["workplan_reset", `P${hashSentence.slice(1)}`],
  ["workplan_checkpoint", `P${hashSentence.slice(1)}`],
  ["workplan_compact", `In apply mode, ${hashSentence}`],
];
const d41Changes: Array<{ tool: string; path: string[]; previous: unknown }> = [];
for (const [name, sentence] of d41Sentences) {
  const tool = tools.find((t) => t.name === name)!;
  d41Changes.push({ tool: name, path: ["description"], previous: tool.description });
  tool.description = `${tool.description} ${sentence}`;
}
// Approved design change D.4.3 (docs/contracts.md §18, 2026-10-01):
// workplan_checkpoint merge mode. Each change records its previous value
// (the D.4.1 description, the reference otherwise) so the test can restore
// it before the d4_1 changes; the two properties are appended.
const checkpoint = tools.find((t) => t.name === "workplan_checkpoint")! as { description: string; input: any };
const d43Changes: Array<{ tool: string; path: string[]; previous: unknown }> = [];
const d43Change = (path: string[], value: unknown) => {
  let cur: any = checkpoint;
  for (const key of path.slice(0, -1)) cur = cur[key];
  d43Changes.push({ tool: "workplan_checkpoint", path, previous: cur[path[path.length - 1]] });
  cur[path[path.length - 1]] = value;
};
const mergeSentence = "Without merge=true it replaces the whole checkpoint (omitted fields become empty); to update it, pass merge=true (omitted fields keep their stored values) or read the current checkpoint first.";
const checkpointHashSentence = `P${hashSentence.slice(1)}`;
d43Change(["description"], `${checkpoint.description.slice(0, -checkpointHashSentence.length)}${mergeSentence} ${checkpointHashSentence}`);
d43Change(["input", "properties", "summary", "description"], `${checkpoint.input.properties.summary.description} (required unless merge=true; never copy a null/withheld value from workplan_resume)`);
d43Change(["input", "properties", "nextAction", "description"], `${checkpoint.input.properties.nextAction.description} (required unless merge=true)`);
d43Change(["input", "required"], ["id"]);
checkpoint.input.properties.merge = {
  description: "Keep the stored checkpoint's value of every omitted field (summary, nextAction, phase/step, blockers, recentValidation, guardrails, references); given fields replace theirs",
  type: "boolean",
};
checkpoint.input.properties.appendValidation = {
  description: "Validation line(s) appended to recentValidation (exact duplicates skipped); use with merge=true to add evidence without rewriting the checkpoint",
  anyOf: [{ type: "string" }, { type: "array", items: { type: "string" } }],
};
console.log(JSON.stringify({
  source: "reference workplan-tools native registration (generated; do not edit)",
  tools,
  d1: {
    note: "Approved design change D.1 (docs/contracts.md §11 item B, 2026-09-30). Removing this key and every listed addition reproduces the reference snapshot byte-for-byte (sha256 below).",
    referenceSha256: createHash("sha256").update(reference).digest("hex"),
    additions: [{ tool: "workplan_read", property: "includeNotes" }],
  },
  d3: {
    note: "Approved design change D.3 (docs/contracts.md §13 item 1, 2026-09-30). Removing this key and the d1 key, every listed addition, and restoring each listed change to its reference value reproduces the reference snapshot byte-for-byte (d1.referenceSha256).",
    additions: [{ tool: "workplan_reset", property: "previewToken" }, { tool: "workplan_reset", property: "confirmation" }],
    changes,
  },
  d4: {
    note: "Approved design change D.4 (docs/contracts.md §15, 2026-09-30): the note rollover selector of compaction (spec 06 P3). Removing this key with the d1/d3 keys, every listed addition, and restoring each d3 change reproduces the reference snapshot byte-for-byte (d1.referenceSha256).",
    additions: [{ tool: "workplan_compact", property: "noteRollover" }, { tool: "workplan_compact_preview", property: "noteRollover" }],
  },
  d4_1: {
    note: "Approved design change D.4.1 (docs/contracts.md §16, 2026-10-01): one sentence about expectedHash on the six mutating tools' descriptions. Restoring each listed change to its previous value (before the d3 changes), then applying the d4/d3/d1 reversal, reproduces the reference snapshot byte-for-byte (d1.referenceSha256).",
    changes: d41Changes,
  },
  d4_3: {
    note: "Approved design change D.4.3 (docs/contracts.md §18, 2026-10-01): workplan_checkpoint merge mode. Removing the listed additions and restoring each listed change to its previous value (before the d4_1 changes), then applying the d4_1/d4/d3/d1 reversal, reproduces the reference snapshot byte-for-byte (d1.referenceSha256).",
    additions: [{ tool: "workplan_checkpoint", property: "merge" }, { tool: "workplan_checkpoint", property: "appendValidation" }],
    changes: d43Changes,
  },
}, null, 2));
