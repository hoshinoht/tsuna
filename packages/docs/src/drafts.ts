import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";

export type DraftStatus = "draft" | "compiled";

export interface DraftEntry {
  title: string;
  preset: string;
  created_at: string;
  last_modified: string;
  source_markdown?: string;
  status: DraftStatus;
}

export interface Registry {
  drafts: Record<string, DraftEntry>;
}

export const DOC_ID_PATTERN = /^[a-z]+-[a-z]+-\d+(-\d+)?$/;

const ADJECTIVES = [
  "amber", "ancient", "autumn", "azure", "bold", "brisk", "calm", "cedar", "clear", "coastal",
  "cold", "coral", "crimson", "crisp", "dappled", "dawn", "deep", "dewy", "distant", "dusky",
  "early", "emerald", "evening", "fern", "fleet", "foggy", "frosty", "gentle", "gilded", "golden",
  "green", "hazel", "hidden", "hollow", "icy", "ivory", "jade", "late", "leafy", "lively",
  "lunar", "mellow", "misty", "mossy", "night", "northern", "pale", "quiet", "rainy", "rapid",
  "rocky", "rustic", "sandy", "silent", "silver", "snowy", "solar", "spring", "still", "stormy",
  "summer", "sunny", "tidal", "twilight", "velvet", "verdant", "wild", "windy", "winter", "young",
];

const NOUNS = [
  "acorn", "alder", "arch", "aspen", "bay", "beacon", "birch", "bloom", "bluff", "branch",
  "brook", "butte", "canyon", "cedar", "cliff", "cloud", "clover", "comet", "cove", "creek",
  "crest", "dale", "delta", "dune", "ember", "estuary", "falls", "fen", "field", "fjord",
  "flint", "forest", "frost", "glade", "glen", "grove", "harbor", "heath", "heron", "hill",
  "island", "lagoon", "lake", "leaf", "marsh", "meadow", "mesa", "moon", "moss", "oak",
  "orchard", "otter", "peak", "pebble", "pine", "pond", "prairie", "rain", "reed", "reef",
  "ridge", "river", "shore", "sky", "spruce", "stone", "stream", "summit", "thicket", "valley",
  "willow", "wren",
];

export const DRAFT_WORDS = { adjectives: ADJECTIVES, nouns: NOUNS } as const;

export function isValidDocId(id: string): boolean {
  return DOC_ID_PATTERN.test(id);
}

function pick<T>(list: readonly T[], random: () => number): T {
  const index = Math.min(list.length - 1, Math.floor(random() * list.length));
  return list[index] as T;
}

export function generateDocId(existing: Set<string> | Record<string, unknown>, random: () => number = Math.random, now: () => number = Date.now): string {
  const taken = existing instanceof Set ? existing : new Set(Object.keys(existing));
  const make = () => `${pick(ADJECTIVES, random)}-${pick(NOUNS, random)}-${100 + Math.min(899, Math.floor(random() * 900))}`;
  let id = make();
  for (let attempt = 1; taken.has(id) && attempt < 100; attempt++) id = make();
  if (!taken.has(id)) return id;
  return `${make()}-${now()}`;
}

export interface DraftPaths {
  base: string;
  registry: string;
}

export function draftPaths(workspace: string): DraftPaths {
  assertWorkspace(workspace);
  return { base: join(workspace, ".opencode", "docs"), registry: join(workspace, ".opencode", "docs-registry.json") };
}

export function assertWorkspace(workspace: string): void {
  if (!workspace || workspace === "/") throw new Error("Cannot determine project directory. Please run from a project directory.");
}

export function readRegistry(path: string): Registry {
  try {
    const parsed = JSON.parse(readFileSync(path, "utf8")) as unknown;
    if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) {
      const drafts = (parsed as { drafts?: unknown }).drafts;
      if (drafts && typeof drafts === "object" && !Array.isArray(drafts)) return { drafts: drafts as Record<string, DraftEntry> };
    }
  } catch {
    // Missing or corrupt registries are treated as empty.
  }
  return { drafts: {} };
}

export function writeRegistry(path: string, registry: Registry): void {
  mkdirSync(dirname(path), { recursive: true });
  writeFileSync(path, `${JSON.stringify(registry, null, 2)}\n`);
}

export function escapeYamlDouble(value: string): string {
  return value.replace(/\\/g, "\\\\").replace(/"/g, '\\"').replace(/\r?\n/g, " ");
}

export function frontMatter(title: string, now: Date): string {
  return `---\ntitle: "${escapeYamlDouble(title)}"\ndate: "${now.toISOString().slice(0, 10)}"\n---\n\n`;
}

export interface AuthorInput {
  name: string;
  sit_id?: string;
  glasgow_id?: string;
}

/** Converts the `authors` JSON argument into the YAML metadata file body. */
export function authorsYaml(json: string): string {
  let parsed: unknown;
  try {
    parsed = JSON.parse(json);
  } catch (error) {
    throw new Error(`Invalid authors JSON: ${error instanceof Error ? error.message : String(error)}`);
  }
  if (!Array.isArray(parsed)) throw new Error("Invalid authors JSON: expected an array of {name, sit_id?, glasgow_id?}");
  const lines = ["authors:"];
  parsed.forEach((item, index) => {
    if (!item || typeof item !== "object" || typeof (item as AuthorInput).name !== "string") {
      throw new Error(`Invalid authors JSON: entry ${index} must be an object with a string 'name'`);
    }
    const author = item as AuthorInput;
    lines.push(`  - name: "${escapeYamlDouble(author.name)}"`);
    if (author.sit_id) lines.push(`    sit-id: "${escapeYamlDouble(String(author.sit_id))}"`);
    if (author.glasgow_id) lines.push(`    glasgow-id: "${escapeYamlDouble(String(author.glasgow_id))}"`);
  });
  return `${lines.join("\n")}\n`;
}
