import type { ExtensionAPI } from "@oh-my-pi/pi-coding-agent";
import { TOOL_DESCRIPTIONS, TOOL_NAMES, canonicalProjectRoot, executeTool } from "../packages/docs/src/register";
import { makeRoots } from "../packages/docs/src/paths";
import { TOOL_SCHEMAS, type ObjectSchema } from "../packages/docs/src/schemas";
import { DocsService } from "../packages/docs/src/service";

function parametersFor(pi: ExtensionAPI, schema: ObjectSchema) {
  const z = pi.zod;
  const fields: Record<string, never> = {};
  for (const [name, property] of Object.entries(schema.properties)) {
    const field = property.type === "boolean"
      ? z.boolean().describe(property.description)
      : property.enum
        ? z.enum([...property.enum] as [string, ...string[]]).describe(property.description)
        : z.string().describe(property.description);
    fields[name] = (schema.required.includes(name) ? field : field.optional()) as never;
  }
  return z.object(fields).strict();
}

function throwIfAborted(signal: AbortSignal | undefined): void {
  if (!signal?.aborted) return;
  throw signal.reason instanceof Error ? signal.reason : new Error("Document operation cancelled.");
}

export default function docsExtension(pi: ExtensionAPI): void {
  const services = new Map<string, DocsService>();

  async function serviceFor(cwd: string): Promise<DocsService> {
    const workspace = await canonicalProjectRoot(cwd);
    const existing = services.get(workspace);
    if (existing) return existing;
    const service = new DocsService(makeRoots(workspace));
    services.set(workspace, service);
    return service;
  }

  for (const name of TOOL_NAMES) {
    pi.registerTool({
      name,
      label: name,
      description: TOOL_DESCRIPTIONS[name],
      parameters: parametersFor(pi, TOOL_SCHEMAS[name]),
      strict: true,
      async execute(_toolCallId, input, signal, _onUpdate, ctx) {
        throwIfAborted(signal);
        const service = await serviceFor(ctx.cwd);
        const text = await executeTool(service, name, input, { signal });
        return {
          content: [{ type: "text", text }],
          details: { workspace: service.roots.workspace },
        };
      },
    });
  }
}
