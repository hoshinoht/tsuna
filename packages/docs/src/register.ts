import { realpath } from "node:fs/promises";
import type { InstallSource } from "./install";
import { makeRoots } from "./paths";
import { TOOL_SCHEMAS, validateInput, type ObjectSchema, type ToolName } from "./schemas";
import {
  DocsService,
  type CompileArgs,
  type CompileLatexArgs,
  type ConvertArgs,
  type CreateArgs,
  type DocsDeps,
  type DraftArgs,
} from "./service";

export const PLUGIN_ID = "docs";

export const TOOL_DESCRIPTIONS: Record<ToolName, string> = {
  docs_create: "One-shot PDF from a markdown file using a preset (school-report, eisvogel, or a user/project YAML preset).",
  docs_draft: "Create a document draft with a unique ID under .opencode/docs/ for iterative editing; compile it later with docs_compile.",
  docs_list_drafts: "List the project's document drafts with title, preset, status and path.",
  docs_delete_draft: "Delete a document draft and its files. Only use when the user asks for it.",
  docs_compile: "Compile a draft to PDF with its stored preset. Supports metadata overrides, bibliography/citation style, and school-report title page fields (logo, course, project_title, group, version, project_topic_id, authors).",
  docs_compile_latex: "Compile an existing .tex file to PDF with a single pdflatex pass (no bibtex, no reruns).",
  docs_convert: "Convert a document between formats with pandoc (pdf, docx, odt, markdown, html, latex).",
  docs_presets_list: "List built-in, user and project document presets.",
  docs_presets_show: "Show a preset's details: source, template availability, citations, logos and required fields.",
  docs_templates_list: "List available pandoc templates and CSL citation styles, with where each comes from.",
  docs_templates_install: "Install a third-party template (eisvogel) or CSL citation style (csl-ieee, csl-apa, csl-acm) into the user pandoc dir. Never overwrites or shadows a bundled template unless force is true.",
};

export const TOOL_NAMES = Object.keys(TOOL_SCHEMAS) as ToolName[];

export interface ToolCallContext {
  signal?: AbortSignal;
}

export interface ToolRegistration {
  name: string;
  description: string;
  input: ObjectSchema;
  options?: { codemode: boolean };
  execute(input: unknown, context: ToolCallContext): Promise<{ content: string }>;
}

/** Minimal slice of the OpenCode V2 plugin context this plugin relies on. */
export interface DocsPluginContext {
  location: { project: { directory: string } };
  tool: {
    transform(edit: (editor: { add(tool: ToolRegistration): void }) => void): Promise<{ dispose(): Promise<void> | void }>;
  };
}

export async function canonicalProjectRoot(directory: string): Promise<string> {
  try {
    return await realpath(directory);
  } catch {
    return directory;
  }
}

export function executeTool(service: DocsService, name: ToolName, rawInput: unknown, context: ToolCallContext = {}): Promise<string> | string {
  const input = validateInput(name, rawInput);
  const options = context.signal ? { signal: context.signal } : {};
  switch (name) {
    case "docs_draft":
      return service.draft(input as unknown as DraftArgs);
    case "docs_list_drafts":
      return service.listDrafts();
    case "docs_delete_draft":
      return service.deleteDraft(input as { doc_id: string });
    case "docs_compile":
      return service.compile(input as unknown as CompileArgs, options);
    case "docs_create":
      return service.create(input as unknown as CreateArgs, options);
    case "docs_convert":
      return service.convert(input as unknown as ConvertArgs, options);
    case "docs_compile_latex":
      return service.compileLatex(input as unknown as CompileLatexArgs, options);
    case "docs_presets_list":
      return service.presetsList();
    case "docs_presets_show":
      return service.presetsShow(input as { preset_name: string });
    case "docs_templates_list":
      return service.templatesList();
    case "docs_templates_install":
      return service.templatesInstall(input as { source: InstallSource; force?: boolean }, options);
  }
}

export async function setupDocsPlugin(ctx: DocsPluginContext, deps: Partial<DocsDeps> = {}, rootOverrides: Parameters<typeof makeRoots>[1] = {}) {
  const workspace = await canonicalProjectRoot(ctx.location.project.directory);
  const service = new DocsService(makeRoots(workspace, rootOverrides), deps);
  const registration = await ctx.tool.transform((editor) => {
    for (const name of TOOL_NAMES) {
      editor.add({
        name,
        description: TOOL_DESCRIPTIONS[name],
        input: TOOL_SCHEMAS[name],
        options: { codemode: true },
        execute: async (input, context) => ({ content: await executeTool(service, name, input, context) }),
      });
    }
  });
  return {
    service,
    dispose: async () => {
      await registration.dispose();
    },
  };
}
