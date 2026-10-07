import { CITATION_STYLES } from "./citations";
import { INSTALL_SOURCES } from "./install";
import { CONVERT_FORMATS } from "./service";

type PropertySchema =
  | { type: "string"; description: string; enum?: readonly string[] }
  | { type: "boolean"; description: string };

export interface ObjectSchema {
  type: "object";
  additionalProperties: false;
  properties: Record<string, PropertySchema>;
  required: string[];
}

function str(description: string): PropertySchema {
  return { type: "string", description };
}

function oneOf(values: readonly string[], description: string): PropertySchema {
  return { type: "string", enum: values, description };
}

function object(properties: Record<string, PropertySchema>, required: string[] = []): ObjectSchema {
  return { type: "object", additionalProperties: false, properties, required };
}

const citationStyle = oneOf(
  CITATION_STYLES,
  "Citation handling: default (preset/refs.bib driven), none (treat @ as text), ieee, apa or acm (require the matching CSL; install with docs_templates_install).",
);
const metadataNote = " Passed as a raw pandoc -V variable (not markdown-parsed; LaTeX specials such as & are not escaped). Prefer YAML front matter for rich text.";

export const TOOL_SCHEMAS = {
  docs_draft: object(
    {
      title: str("Document title (written into the draft's YAML front matter)."),
      preset: str("Preset name used when compiling (e.g. school-report, eisvogel). Not validated until compile."),
      initial_content: str("Initial markdown content."),
      source_markdown: str("Path to an existing markdown file to start from (relative paths resolve against the project root). A refs.bib next to it is copied."),
    },
    ["title", "preset"],
  ),
  docs_list_drafts: object({}),
  docs_delete_draft: object({ doc_id: str("Draft ID, e.g. misty-harbor-123.") }, ["doc_id"]),
  docs_compile: object(
    {
      doc_id: str("Draft ID to compile."),
      output_path: str("Output PDF path (relative paths resolve against the project root). Defaults to <draft dir>/<doc_id>.pdf."),
      author: str(`Author override.${metadataNote}`),
      date: str(`Date override.${metadataNote}`),
      subtitle: str(`Subtitle override.${metadataNote}`),
      abstract: str(`Abstract override.${metadataNote}`),
      keywords: str(`Keywords, comma separated.${metadataNote}`),
      bibliography: str("Path to a .bib file (relative paths resolve against the draft directory). Defaults to the draft's non-empty refs.bib."),
      citation_style: citationStyle,
      logo: str("Title-page logo for school-report: sit, uofg or both."),
      course: str("School report: course name/code."),
      project_title: str("School report: project title."),
      group: str("School report: group name/number."),
      version: str("School report: document version."),
      project_topic_id: str("School report: project topic ID."),
      authors: str('School report author table as a JSON array string: [{"name":"...","sit_id":"...","glasgow_id":"..."}].'),
    },
    ["doc_id"],
  ),
  docs_create: object(
    {
      input_path: str("Markdown input file (relative paths resolve against the project root)."),
      preset: str("Preset name (see docs_presets_list)."),
      output_dir: str("Output directory (relative paths resolve against the project root). Defaults to the input's directory."),
      logo: str("Logo option for presets with logos (school-report: sit, uofg or both)."),
      title: str(`Title override.${metadataNote}`),
      author: str(`Author override.${metadataNote}`),
      date: str(`Date override.${metadataNote}`),
      subtitle: str(`Subtitle override.${metadataNote}`),
      abstract: str(`Abstract override.${metadataNote}`),
      keywords: str(`Keywords, comma separated.${metadataNote}`),
      bibliography: str("Path to a .bib file (relative paths resolve against the input's directory). Defaults to a non-empty refs.bib next to the input."),
      citation_style: citationStyle,
    },
    ["input_path", "preset"],
  ),
  docs_convert: object(
    {
      input_path: str("Input file (absolute, or relative to the project root)."),
      output_format: oneOf(CONVERT_FORMATS, "Target format; also used literally as the output file extension."),
      from_format: str("Pandoc input format (auto-detected from the extension when omitted)."),
      output_dir: str("Output directory (relative paths resolve against the project root). Defaults to the input's directory."),
    },
    ["input_path", "output_format"],
  ),
  docs_compile_latex: object(
    {
      file_path: str("Path to a .tex file (absolute, or relative to the project root). Compiled with a single pdflatex pass."),
      output_dir: str("Output directory. Defaults to the .tex file's directory."),
    },
    ["file_path"],
  ),
  docs_presets_list: object({}),
  docs_presets_show: object({ preset_name: str("Preset name.") }, ["preset_name"]),
  docs_templates_list: object({}),
  docs_templates_install: object(
    {
      source: oneOf(INSTALL_SOURCES, "What to install: eisvogel (template), or a CSL citation style (csl-ieee, csl-apa, csl-acm)."),
      force: { type: "boolean", description: "Overwrite an existing file or shadow a bundled template. Defaults to false." },
    },
    ["source"],
  ),
} as const satisfies Record<string, ObjectSchema>;

export type ToolName = keyof typeof TOOL_SCHEMAS;

export function validateInput(tool: ToolName, input: unknown): Record<string, unknown> {
  const schema: ObjectSchema = TOOL_SCHEMAS[tool];
  const value = input === undefined || input === null ? {} : input;
  if (typeof value !== "object" || Array.isArray(value)) throw new Error(`Invalid ${tool} input: expected an object`);
  const record = value as Record<string, unknown>;
  const problems: string[] = [];
  for (const key of Object.keys(record)) {
    if (!Object.hasOwn(schema.properties, key)) problems.push(`unknown argument '${key}'`);
  }
  for (const key of schema.required) {
    if (record[key] === undefined || record[key] === null) problems.push(`missing required argument '${key}'`);
  }
  const out: Record<string, unknown> = {};
  for (const [key, property] of Object.entries(schema.properties)) {
    const raw = record[key];
    if (raw === undefined || raw === null) continue;
    if (property.type === "boolean") {
      if (typeof raw !== "boolean") problems.push(`'${key}' must be a boolean`);
      else out[key] = raw;
      continue;
    }
    if (typeof raw !== "string") {
      problems.push(`'${key}' must be a string`);
      continue;
    }
    if (property.enum) {
      const normalized = key === "citation_style" ? raw.toLowerCase() : raw;
      if (!property.enum.includes(normalized)) {
        problems.push(`'${key}' must be one of ${property.enum.join(", ")} (got '${raw}')`);
        continue;
      }
      out[key] = normalized;
      continue;
    }
    if (schema.required.includes(key) && raw.trim().length === 0) {
      problems.push(`'${key}' must not be empty`);
      continue;
    }
    out[key] = raw;
  }
  if (problems.length > 0) throw new Error(`Invalid ${tool} input: ${problems.join("; ")}`);
  return out;
}
