import { AgentRegistry, type ExtensionAPI, type ExtensionContext } from "@oh-my-pi/pi-coding-agent";
import { cfgSkillsCustomDirectories } from "@oh-my-pi/pi-coding-agent/extensibility/settings";
import { loadProjectContext } from "../lib/project-context";

export default function projectContext(pi: ExtensionAPI) {
  let addedPaths: string[] = [];
  async function refresh(ctx: ExtensionContext) {
    const session = AgentRegistry.global().get(ctx.agent.id)?.session;
    if (!session) throw new Error("Project context requires the current OMP session");
    const { skillPaths } = await loadProjectContext(ctx.cwd);
    const existing = cfgSkillsCustomDirectories.get(session.settings).filter(path => !addedPaths.includes(path));
    addedPaths = skillPaths.filter(path => !existing.includes(path));
    // Keep the project's skill paths session-local; the native loader handles names and exclusions.
    cfgSkillsCustomDirectories.override(session.settings, [...existing, ...addedPaths]);
    await session.refreshSkills();
  }
  pi.on("session_start", (_event, ctx) => refresh(ctx));
  pi.on("session_switch", (_event, ctx) => refresh(ctx));
  pi.on("before_agent_start", async (event, ctx) => {
    const { instructions } = await loadProjectContext(ctx.cwd);
    if (!instructions.length) return;
    const blocks = instructions.map(({ path, content }) => `### ${path}\n\n${content}`);
    return { systemPrompt: [...event.systemPrompt,
      "## Project tool instructions\n\nApply these project instructions under the existing instruction precedence. More specific project directories override ancestor directories.\n\n" + blocks.join("\n\n"),
    ] };
  });
}
