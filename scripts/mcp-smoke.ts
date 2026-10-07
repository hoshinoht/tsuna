import { mkdtemp, rm } from "node:fs/promises";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { MCPManager } from "@oh-my-pi/pi-coding-agent/mcp/manager";
import { root } from "./setup";

const workspace = await mkdtemp(join(tmpdir(), "hoshi-mcp-smoke-"));
const manager = new MCPManager(workspace);
try {
  const configs = {
    workplan: { type: "stdio" as const, command: join(root, "vendor/shiori/shiori"), args: ["mcp", "--write-approval", "client"] },
    gofetch: { type: "stdio" as const, command: join(root, "mcps/gofetch-mcp/bin/gofetch") },
    "researcher-mcp": { type: "stdio" as const, command: join(root, "mcps/researcher-mcp/bin/researcher-mcp") },
    "lsp-tools": { type: "stdio" as const, command: "node", args: [join(root, "mcps/lsp-tools-mcp/mcp-no-idle.mjs")] },
  };
  const result = await manager.connectServers(configs, {}, undefined, 0);
  if (result.errors.size) throw new Error(JSON.stringify([...result.errors]));
  const tools = manager.getTools();
  for (const [server, prefix] of [["workplan", "mcp__workplan_"], ["gofetch", "mcp__gofetch_"], ["researcher-mcp", "mcp__researcher_mcp_"], ["lsp-tools", "mcp__lsp_tools_"]]) {
    const count = tools.filter(t => t.name.startsWith(prefix)).length;
    if (!count) throw new Error(`${server}: no tools discovered`);
    console.log(`${server}: ${count} tools discovered`);
  }
  const list = tools.find(t => t.name === "mcp__workplan_list");
  if (!list) throw new Error("Shiori workplan_list not registered");
  const output = await list.execute("smoke-workplan-list", {}, undefined, { cwd: workspace } as never, undefined);
  console.log(`Shiori current-project read: ${JSON.stringify(output.content)}`);
} finally {
  await manager.disconnectAll();
  await rm(workspace, { recursive: true, force: true });
}
