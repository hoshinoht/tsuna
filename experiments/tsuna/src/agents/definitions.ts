/**
 * External agent definitions.
 *
 * Definitions are configuration and prompt content supplied by agent packs in
 * explicitly configured directories. The harness owns their execution; packs
 * never import harness code and the runtime never embeds their prompt bodies.
 *
 * Pack layout:
 *   <pack>/pack.json          { "name": "...", "version": "...", "description"?: "..." }
 *   <pack>/agents/<name>.md   YAML frontmatter + prompt body
 */
import { createHash } from "node:crypto";
import { existsSync, readdirSync, readFileSync, realpathSync, statSync } from "node:fs";
import { basename, join } from "node:path";
import { parse as parseYaml } from "yaml";
import type { PermissionRule } from "../policy/rules.ts";

export type ReasoningLevel = "off" | "minimal" | "low" | "medium" | "high" | "xhigh";
export const REASONING_LEVELS: readonly ReasoningLevel[] = ["off", "minimal", "low", "medium", "high", "xhigh"];

export interface ReasoningPreference {
	default: ReasoningLevel;
	min?: ReasoningLevel;
	max?: ReasoningLevel;
}

export interface AgentDefinition {
	name: string;
	description: string;
	/** Logical model entry name from the provider configuration. */
	model: string;
	reasoning: ReasoningPreference;
	/** Tool catalog requested by the definition (still subject to policy). */
	tools: string[];
	/** Roles this agent may spawn; empty means it cannot delegate. */
	spawns: string[];
	/** Ordered last-match-wins permission rules for this role. */
	permissions: PermissionRule[];
	/** Whether the role may be the primary (interactive) agent. */
	primary: boolean;
	/** Optional JSON schema the terminal yield `data` must satisfy. */
	output?: { schema?: Record<string, unknown> };
	prompt: string;
	provenance: DefinitionProvenance;
}

export interface DefinitionProvenance {
	pack: string;
	packVersion: string;
	packDir: string;
	file: string;
	/** sha256 of the definition file bytes. */
	sha256: string;
}

export class DefinitionError extends Error {}

const NAME = /^[a-z][a-z0-9-]{0,47}$/;
const ALLOWED_KEYS = new Set([
	"name", "description", "model", "reasoning", "tools", "spawns", "permissions", "primary", "output",
]);

function splitFrontmatter(text: string, file: string): { meta: Record<string, unknown>; body: string } {
	const match = /^---\r?\n([\s\S]*?)\r?\n---\r?\n?([\s\S]*)$/.exec(text);
	if (!match) throw new DefinitionError(`${file}: missing YAML frontmatter`);
	const meta = parseYaml(match[1]!);
	if (typeof meta !== "object" || meta === null || Array.isArray(meta)) {
		throw new DefinitionError(`${file}: frontmatter must be a mapping`);
	}
	return { meta: meta as Record<string, unknown>, body: match[2]!.trim() };
}

function level(value: unknown, file: string, field: string): ReasoningLevel {
	if (typeof value !== "string" || !REASONING_LEVELS.includes(value as ReasoningLevel)) {
		throw new DefinitionError(`${file}: ${field} must be one of ${REASONING_LEVELS.join(", ")}`);
	}
	return value as ReasoningLevel;
}

function stringList(value: unknown, file: string, field: string): string[] {
	if (value === undefined || value === false) return [];
	if (!Array.isArray(value) || value.some(item => typeof item !== "string" || item.length === 0)) {
		throw new DefinitionError(`${file}: ${field} must be a list of non-empty strings`);
	}
	return [...value] as string[];
}

function parseRules(value: unknown, file: string): PermissionRule[] {
	if (!Array.isArray(value) || value.length === 0) {
		throw new DefinitionError(`${file}: permissions must be a non-empty ordered list`);
	}
	return value.map((rule, index) => {
		if (Array.isArray(rule) && rule.length === 3 && rule.every(part => typeof part === "string")) {
			const [effect, action, resource] = rule as [string, string, string];
			if (effect !== "allow" && effect !== "ask" && effect !== "deny") {
				throw new DefinitionError(`${file}: permissions[${index}] has invalid effect ${effect}`);
			}
			return { effect, action, resource };
		}
		throw new DefinitionError(`${file}: permissions[${index}] must be [effect, action, resource]`);
	});
}

export function parseDefinition(text: string, file: string, pack: { name: string; version: string; dir: string }): AgentDefinition {
	const { meta, body } = splitFrontmatter(text, file);
	for (const key of Object.keys(meta)) {
		if (!ALLOWED_KEYS.has(key)) throw new DefinitionError(`${file}: unknown frontmatter key "${key}"`);
	}
	const name = meta.name;
	if (typeof name !== "string" || !NAME.test(name)) throw new DefinitionError(`${file}: invalid or missing name`);
	if (basename(file, ".md") !== name) {
		throw new DefinitionError(`${file}: file name must match agent name "${name}" (ambiguous identity)`);
	}
	if (typeof meta.description !== "string" || meta.description.trim() === "") {
		throw new DefinitionError(`${file}: description is required`);
	}
	if (typeof meta.model !== "string" || meta.model.trim() === "") throw new DefinitionError(`${file}: model is required`);
	if (body.length === 0) throw new DefinitionError(`${file}: prompt body is empty`);
	const reasoningRaw = meta.reasoning ?? "medium";
	let reasoning: ReasoningPreference;
	if (typeof reasoningRaw === "string") reasoning = { default: level(reasoningRaw, file, "reasoning") };
	else if (typeof reasoningRaw === "object" && reasoningRaw !== null && !Array.isArray(reasoningRaw)) {
		const r = reasoningRaw as Record<string, unknown>;
		reasoning = {
			default: level(r.default, file, "reasoning.default"),
			min: r.min === undefined ? undefined : level(r.min, file, "reasoning.min"),
			max: r.max === undefined ? undefined : level(r.max, file, "reasoning.max"),
		};
		const idx = (l?: ReasoningLevel) => (l ? REASONING_LEVELS.indexOf(l) : -1);
		if ((reasoning.min && idx(reasoning.min) > idx(reasoning.default)) || (reasoning.max && idx(reasoning.max) < idx(reasoning.default))) {
			throw new DefinitionError(`${file}: reasoning.default must lie within min..max`);
		}
	} else throw new DefinitionError(`${file}: reasoning must be a level or {default,min,max}`);
	let output: AgentDefinition["output"];
	if (meta.output !== undefined) {
		if (typeof meta.output !== "object" || meta.output === null || Array.isArray(meta.output)) {
			throw new DefinitionError(`${file}: output must be a mapping`);
		}
		output = meta.output as AgentDefinition["output"];
	}
	const spawns = stringList(meta.spawns, file, "spawns");
	for (const s of spawns) if (!NAME.test(s)) throw new DefinitionError(`${file}: invalid spawn target ${s}`);
	return {
		name,
		description: meta.description.trim(),
		model: meta.model.trim(),
		reasoning,
		tools: stringList(meta.tools, file, "tools"),
		spawns,
		permissions: parseRules(meta.permissions, file),
		primary: meta.primary === true,
		output,
		prompt: body,
		provenance: {
			pack: pack.name,
			packVersion: pack.version,
			packDir: pack.dir,
			file,
			sha256: createHash("sha256").update(text).digest("hex"),
		},
	};
}

function readPack(dir: string): { name: string; version: string; dir: string } {
	const manifestPath = join(dir, "pack.json");
	if (!existsSync(manifestPath)) throw new DefinitionError(`${dir}: missing pack.json`);
	const manifest = JSON.parse(readFileSync(manifestPath, "utf8")) as Record<string, unknown>;
	if (typeof manifest.name !== "string" || typeof manifest.version !== "string") {
		throw new DefinitionError(`${manifestPath}: name and version are required`);
	}
	return { name: manifest.name, version: manifest.version, dir };
}

export class DefinitionCatalog {
	private readonly byName = new Map<string, AgentDefinition>();

	constructor(definitions: Iterable<AgentDefinition>) {
		for (const definition of definitions) {
			const existing = this.byName.get(definition.name);
			if (existing) {
				throw new DefinitionError(
					`duplicate agent name "${definition.name}" in ${existing.provenance.file} and ${definition.provenance.file}`,
				);
			}
			this.byName.set(definition.name, definition);
		}
		for (const definition of this.byName.values()) {
			for (const target of definition.spawns) {
				if (!this.byName.has(target)) {
					throw new DefinitionError(`${definition.provenance.file}: spawns unknown agent "${target}"`);
				}
			}
		}
	}

	get(name: string): AgentDefinition | undefined {
		return this.byName.get(name);
	}

	require(name: string): AgentDefinition {
		const definition = this.byName.get(name);
		if (!definition) throw new DefinitionError(`unknown agent "${name}"`);
		return definition;
	}

	list(): AgentDefinition[] {
		return [...this.byName.values()];
	}
}

/**
 * Load every pack in `packDirs`. Only explicitly configured directories are
 * read; there is no discovery of user-level or project-level agent folders.
 */
export function loadDefinitions(packDirs: readonly string[]): DefinitionCatalog {
	const definitions: AgentDefinition[] = [];
	const seenPacks = new Map<string, string>();
	for (const raw of packDirs) {
		const dir = realpathSync.native(raw);
		if (!statSync(dir).isDirectory()) throw new DefinitionError(`${raw}: not a directory`);
		const pack = readPack(dir);
		const prior = seenPacks.get(pack.name);
		if (prior) throw new DefinitionError(`duplicate pack name "${pack.name}" in ${prior} and ${dir}`);
		seenPacks.set(pack.name, dir);
		const agentsDir = join(dir, "agents");
		for (const entry of readdirSync(agentsDir).sort()) {
			if (!entry.endsWith(".md")) continue;
			const file = join(agentsDir, entry);
			definitions.push(parseDefinition(readFileSync(file, "utf8"), file, pack));
		}
	}
	return new DefinitionCatalog(definitions);
}
