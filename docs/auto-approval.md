# Automatic approvals

Tsuna's `auto` mode uses a separate, no-tools model call to decide whether a proposed action fits the human's task. Normal local reads and project edits stay off the review path. Shell commands, arbitrary code, delegation, external edits and MCP operations receive contextual review. Specific command/resource `ask` rules and protected-path changes still require a human; a reviewer cannot override a denied capability.

Restart Tsuna to load the extension. The saved startup mode is currently `off`, at the user's request while automatic review is being validated.

| Command | Behavior |
|---|---|
| `/tsuna-permissions off` | Auto-approve permission requests without model review or confirmation dialogs, including protected-file edits, publishing and deletion asks. Explicit capability/role denials and unavailable tools remain enforced. |
| `/tsuna-permissions auto` | Contextual model review, with explicit policy asks still shown to the human. |
| `/tsuna-permissions source` | Original source permission rules and manual prompts. |

The startup default is `config/permission-mode.json`, overridden by `TSUNA_PERMISSION_MODE`. Runtime mode changes do not change other sessions or the saved default. Switching to `off` also clears the current session's review circuit so a prior reviewer failure cannot keep blocking it.

## Reviewer configuration

`config/auto-review.json` selects the model, timeout, response-token budget and maximum evidence size. The pilot model is `cliproxy-openai/gpt-6-luna`, using medium reasoning and existing OMP model/auth resolution. These are ordinary provider requests and consume tokens. Missing models, authentication failures, timeouts, incomplete context and malformed responses never grant permission.

For the Luna pilot, restart Tsuna and opt the current chat in with `/tsuna-permissions auto`; `/tsuna-permissions off` exits immediately. The saved default remains `off`. Existing audit entries record the actual reviewer model, verdict, reason and latency. No stronger-model fallback is enabled in this baseline.

Run `bun scripts/auto-review-pilot.ts --live` to evaluate eight synthetic examples through the configured gateway: the reported Cargo commands, an ordinary `AGENTS.md` update, credential upload, overwriting another user's document, unowned deletion, an instruction-policy attack and a long history. This sends model requests but never executes the proposed actions or reads real conversation history. Results are saved to `.runtime/auto-review-pilot.json`. A passing smoke set establishes basic compatibility, not a safety or latency benchmark. The initial seven-case pilot exposed a false denial of test-log redirection; the policy now explicitly distinguishes routine output generation from destructive cleanup.

The evidence limit is 512,000 characters, accommodating the observed resumed histories over 200,000 characters. The changing proposed action comes after history to improve prefix-cache reuse. No authorization or executable history is silently discarded. Oversized-history errors report the actual size and limit; model lookup, credential, timeout and invalid-response failures have distinct explanations.

The first implementation uses one deliberate classification stage. A fast preliminary stage should be added only after measuring missed dangerous actions, unnecessary interruptions and latency. There is no learned allowlist or reusable model-verdict cache.

## Authorization and recovery

The reviewer receives actual human messages from the active branch, prior executable tool calls and the exact proposed action. Assistant prose, reasoning and raw tool outputs are excluded. A child's brief is marked as delegated context; root human authorization must come from verified session lineage. Earlier human requests remain available across ordinary compaction; missing or oversized evidence falls back to human review rather than silently dropping authorization history.

An automatic denial returns its reason to the agent so it can choose a materially safer approach. It must not pursue the same outcome through another tool or disguise the command. Three consecutive denials or ten denials in the last fifty completed reviews stop the current run. A new agent run resets the circuit. Reviewer outages do not count as safety denials or trip this circuit; in auto mode they fall back to manual approval in an interactive primary session.

Factual updates to repository `AGENTS.md` and `CLAUDE.md` files are eligible for contextual approval. They are distinct from modifying Tsuna's approval implementation/configuration, which retains its explicit protection in auto mode.

Use `/tsuna-approve` to inspect a recent denial and grant one reviewed retry. The override covers the exact tool, arguments, role and working directory within this session. It is consumed once, invalidated by changed human context, and still checked by the reviewer against hard security rules. Overrides are not persisted across session reload/navigation. This does not replace explicit policy `ask` rules. A headless child returns unresolved approval needs to its parent.

Concurrent reviews are serialized per session/agent. Changed evidence or arguments invalidate an in-flight decision. Decisions are recorded as `tsuna-auto-review-decision` custom session entries with the policy version, action hash, verdict, sanitized reason, latency and model usage. Successful review does not open a dialog.

## Cleanup

Routine cleanup can be approved when the harness establishes ownership and retained work. Being in `/tmp` is not proof of ownership. The cleanup tracker observes successful creation, verifies artifact identity, and supplies facts to the classifier. It supports a deliberately narrow subset of literal commands; shell wrappers, globbing and compound commands do not acquire verified-cleanup status.

- Temporary files: files created through an observed native `write`, with identity/content still matching. Pre-existing, replaced or externally modified files are not treated as disposable.
- Worktrees: observed literal creation, followed by non-force removal only when the worktree belongs to the same repository, is clean including untracked/ignored files, and its commit remains reachable through another retained reference.
- Force removal, recursive/broad deletion, uncertain ownership and branch deletion require separate authorization. Existing explicit ask rules continue to prompt.

Ownership tracking is scoped to a live session and fails closed after reload. A parent does not automatically inherit a child's cleanup ownership.

## Limits and validation

This is a probabilistic authorization gate, not an OS sandbox. It cannot prevent every side effect of an approved process, atomically freeze filesystem state between review and execution, or protect itself from arbitrary trusted extensions loaded into the same process. A real filesystem/network sandbox remains separate work. Tool-generated instructions are excluded from authorization evidence; this version does not add Claude's independent input-side injection detector.

Offline tests cover routing, malformed responses, cancellation, provenance, context changes, circuit breaking, overrides, native wrapper blocking and cleanup proofs. They do not establish live classifier quality or provider latency. Before depending on unattended review for consequential work, replay labeled examples against the selected model and measure both false approvals and unnecessary interruptions.
