import { resolve } from "node:path";
import { workplan_validate } from "../packages/workplan-tools/src/core/validate";

/** Read-only structural check for V2 sessions where custom workplan tools are unavailable. */
export async function checkWorkplan(workspaceRoot: string, id: string) {
  const root = resolve(workspaceRoot);
  try {
    const validation = await workplan_validate.execute(
      { workspaceRoot: root, id },
      { directory: root, worktree: root, metadata() {} } as never,
    );
    const result = JSON.parse(typeof validation === "string" ? validation : validation.output);
    const issues = (result.issues as string[]).flatMap((issue) => issue.split(/; (?=[A-Za-z_$][\w.$[\]-]*:)/));
    return { valid: result.valid === true, issues };
  } catch (error) {
    return { valid: false, issues: [error instanceof Error ? error.message : String(error)] };
  }
}

if (import.meta.main) {
  const [root, id, ...extra] = Bun.argv.slice(2);
  if (!root || !id || extra.length) {
    process.stderr.write("Usage: bun scripts/check-workplan.ts <workspace-root> <workplan-id>\n");
    process.exitCode = 2;
  } else {
    const result = await checkWorkplan(root, id);
    process.stdout.write(`${JSON.stringify({ ...result, gate: "structure", note: "Does not establish readiness, authorization, or completion." }, null, 2)}\n`);
    process.exitCode = result.valid ? 0 : 1;
  }
}
