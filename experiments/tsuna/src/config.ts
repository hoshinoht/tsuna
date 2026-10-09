/**
 * Tsuna-owned configuration.
 *
 * Precedence: CLI flags > config file (`--config`, else `TSUNA_EXPERIMENT_CONFIG`,
 * else `<experiment>/tsuna.config.json`) > built-in defaults. Relative paths
 * resolve against the file that declares them. No other agent ecosystem's
 * configuration is read. Provider configuration is separate from agent
 * definitions: definitions name a logical model entry; providers map it.
 */
import { existsSync, readFileSync } from "node:fs";
import { dirname, isAbsolute, join, resolve } from "node:path";
import { REASONING_LEVELS, type ReasoningLevel } from "./agents/definitions.ts";
import { EXPERIMENT_DIR } from "./paths.ts";
import type { PermissionRule } from "./policy/rules.ts";

export class ConfigError extends Error {}

export interface FixtureProviderConfig {
	type: "fixture";
	/** Module exporting the deterministic model script (trusted config only). */
	script: string;
}

export interface ApiProviderConfig {
	type: "api";
	/** A pi-ai API id, e.g. `openai-completions`, `anthropic-messages`. */
	api: string;
	baseUrl: string;
	/** Name of the environment variable holding the key (never the key itself). */
	apiKeyEnv?: string;
	headers?: Record<string, string>;
}

export type ProviderConfig = FixtureProviderConfig | ApiProviderConfig;

export interface ModelEntry {
	provider: string;
	id: string;
	reasoning: boolean;
	/** Reasoning levels the backend model actually supports (checked, not translated). */
	reasoningLevels?: ReasoningLevel[];
	contextWindow: number;
	maxTokens: number;
	input: ("text" | "image")[];
}

export interface ProvidersConfig {
	providers: Record<string, ProviderConfig>;
	models: Record<string, ModelEntry>;
}

export interface McpServerConfig {
	type: "stdio" | "http";
	enabled: boolean;
	command?: string;
	args?: string[];
	/** Literal env values or `${NAME}` placeholders resolved from `envAllow` only. */
	env?: Record<string, string>;
	envAllow?: string[];
	url?: string;
	headers?: Record<string, string>;
	timeoutMs?: number;
	description?: string;
}

export interface McpConfig {
	servers: Record<string, McpServerConfig>;
}

export interface ImageBudgetConfig {
	maxImages: number;
	maxImageBytes: number;
	pruneTo: number;
}

export interface CacheAdvisoryConfig {
	enabled: boolean;
	riskAfterMinutes: number;
	minCacheReadTokens: number;
}

export interface TsunaConfig {
	file?: string;
	agentPacks: string[];
	providers: ProvidersConfig;
	mcp: McpConfig;
	primaryRole: string;
	approvalMode: "source" | "auto";
	scheduler: { maxConcurrency: number; maxDepth: number };
	harnessDenies: PermissionRule[];
	context: { imageBudget: ImageBudgetConfig; cacheAdvisory: CacheAdvisoryConfig; projectInstructions: boolean };
	/** Absolute path to the supervisor binary (defaults to the experiment build). */
	supervisor: string;
}

export const DEFAULT_SUPERVISOR = join(EXPERIMENT_DIR, ".cache", "cargo-target", "release", "tsuna-supervisor");

function isRecord(value: unknown): value is Record<string, unknown> {
	return typeof value === "object" && value !== null && !Array.isArray(value);
}

function readJson(path: string): unknown {
	try {
		return JSON.parse(readFileSync(path, "utf8"));
	} catch (error) {
		throw new ConfigError(`${path}: ${(error as Error).message}`);
	}
}

function rel(base: string, path: string): string {
	return isAbsolute(path) ? path : resolve(base, path);
}

export function parseProviders(raw: unknown, base: string, where: string): ProvidersConfig {
	if (!isRecord(raw) || !isRecord(raw.providers) || !isRecord(raw.models)) {
		throw new ConfigError(`${where}: providers config needs "providers" and "models" objects`);
	}
	const providers: Record<string, ProviderConfig> = {};
	for (const [name, value] of Object.entries(raw.providers)) {
		if (!isRecord(value)) throw new ConfigError(`${where}: provider ${name} must be an object`);
		if (value.type === "fixture") {
			if (typeof value.script !== "string") throw new ConfigError(`${where}: fixture provider ${name} needs script`);
			providers[name] = { type: "fixture", script: rel(base, value.script) };
		} else if (value.type === "api") {
			if (typeof value.api !== "string" || typeof value.baseUrl !== "string") {
				throw new ConfigError(`${where}: api provider ${name} needs api and baseUrl`);
			}
			if ("apiKey" in value) throw new ConfigError(`${where}: provider ${name}: store keys in the environment (apiKeyEnv), not config`);
			providers[name] = {
				type: "api",
				api: value.api,
				baseUrl: value.baseUrl,
				apiKeyEnv: typeof value.apiKeyEnv === "string" ? value.apiKeyEnv : undefined,
				headers: isRecord(value.headers) ? (value.headers as Record<string, string>) : undefined,
			};
		} else throw new ConfigError(`${where}: provider ${name} has unknown type`);
	}
	const models: Record<string, ModelEntry> = {};
	for (const [name, value] of Object.entries(raw.models)) {
		if (!isRecord(value)) throw new ConfigError(`${where}: model ${name} must be an object`);
		const provider = value.provider;
		if (typeof provider !== "string" || !providers[provider]) throw new ConfigError(`${where}: model ${name} names unknown provider`);
		if (typeof value.id !== "string") throw new ConfigError(`${where}: model ${name} needs id`);
		if (typeof value.contextWindow !== "number" || typeof value.maxTokens !== "number") {
			throw new ConfigError(`${where}: model ${name} needs explicit contextWindow and maxTokens`);
		}
		const levels = value.reasoningLevels;
		if (levels !== undefined && (!Array.isArray(levels) || levels.some(l => !REASONING_LEVELS.includes(l as ReasoningLevel)))) {
			throw new ConfigError(`${where}: model ${name} has invalid reasoningLevels`);
		}
		models[name] = {
			provider,
			id: value.id,
			reasoning: value.reasoning === true,
			reasoningLevels: levels as ReasoningLevel[] | undefined,
			contextWindow: value.contextWindow,
			maxTokens: value.maxTokens,
			input: Array.isArray(value.input) ? (value.input as ("text" | "image")[]) : ["text"],
		};
	}
	return { providers, models };
}

export function parseMcp(raw: unknown, base: string, where: string): McpConfig {
	if (!isRecord(raw) || !isRecord(raw.servers)) throw new ConfigError(`${where}: MCP config needs "servers"`);
	const servers: Record<string, McpServerConfig> = {};
	for (const [name, value] of Object.entries(raw.servers)) {
		if (!/^[a-z][a-z0-9-]{0,31}$/.test(name)) throw new ConfigError(`${where}: invalid MCP server name ${name}`);
		if (!isRecord(value)) throw new ConfigError(`${where}: server ${name} must be an object`);
		const type = value.type;
		if (type !== "stdio" && type !== "http") throw new ConfigError(`${where}: server ${name} type must be stdio or http`);
		if (type === "stdio" && typeof value.command !== "string") throw new ConfigError(`${where}: server ${name} needs command`);
		if (type === "http" && typeof value.url !== "string") throw new ConfigError(`${where}: server ${name} needs url`);
		servers[name] = {
			type,
			enabled: value.enabled !== false,
			command: typeof value.command === "string" ? (/^\.\.?\//.test(value.command) ? rel(base, value.command) : value.command) : undefined,
			// Relative paths ("./x", "../x") resolve against the MCP config file.
			args: Array.isArray(value.args) ? (value.args as string[]).map(arg => (/^\.\.?\//.test(arg) ? rel(base, arg) : arg)) : undefined,
			env: isRecord(value.env) ? (value.env as Record<string, string>) : undefined,
			envAllow: Array.isArray(value.envAllow) ? (value.envAllow as string[]) : undefined,
			url: typeof value.url === "string" ? value.url : undefined,
			headers: isRecord(value.headers) ? (value.headers as Record<string, string>) : undefined,
			timeoutMs: typeof value.timeoutMs === "number" ? value.timeoutMs : undefined,
			description: typeof value.description === "string" ? value.description : undefined,
		};
	}
	return { servers };
}

function section(raw: Record<string, unknown>, key: string, base: string, where: string): { value: unknown; base: string; where: string } {
	const value = raw[key];
	if (typeof value === "string") {
		const file = rel(base, value);
		return { value: readJson(file), base: dirname(file), where: file };
	}
	return { value, base, where: `${where}#${key}` };
}

export function defaultConfig(): TsunaConfig {
	return {
		agentPacks: [],
		providers: { providers: {}, models: {} },
		mcp: { servers: {} },
		primaryRole: "orchestrator",
		approvalMode: "source",
		scheduler: { maxConcurrency: 4, maxDepth: 2 },
		harnessDenies: [],
		context: {
			imageBudget: { maxImages: 12, maxImageBytes: 6 * 1024 * 1024, pruneTo: 0.5 },
			cacheAdvisory: { enabled: true, riskAfterMinutes: 30, minCacheReadTokens: 10_000 },
			projectInstructions: true,
		},
		supervisor: DEFAULT_SUPERVISOR,
	};
}

export function loadConfig(explicit?: string, env: NodeJS.ProcessEnv = process.env): TsunaConfig {
	const config = defaultConfig();
	const candidate = explicit ?? env.TSUNA_EXPERIMENT_CONFIG ?? join(EXPERIMENT_DIR, "tsuna.config.json");
	if (!existsSync(candidate)) {
		if (explicit || env.TSUNA_EXPERIMENT_CONFIG) throw new ConfigError(`config file not found: ${candidate}`);
		return config;
	}
	const file = resolve(candidate);
	const base = dirname(file);
	const raw = readJson(file);
	if (!isRecord(raw)) throw new ConfigError(`${file}: must be an object`);
	config.file = file;
	if (raw.agentPacks !== undefined) {
		if (!Array.isArray(raw.agentPacks) || raw.agentPacks.some(p => typeof p !== "string")) {
			throw new ConfigError(`${file}: agentPacks must be a list of directories`);
		}
		config.agentPacks = (raw.agentPacks as string[]).map(p => rel(base, p));
	}
	if (raw.providers !== undefined) {
		const s = section(raw, "providers", base, file);
		config.providers = parseProviders(s.value, s.base, s.where);
	}
	if (raw.mcp !== undefined) {
		const s = section(raw, "mcp", base, file);
		config.mcp = parseMcp(s.value, s.base, s.where);
	}
	if (typeof raw.primaryRole === "string") config.primaryRole = raw.primaryRole;
	if (raw.approvalMode === "auto" || raw.approvalMode === "source") config.approvalMode = raw.approvalMode;
	if (isRecord(raw.scheduler)) {
		const { maxConcurrency, maxDepth } = raw.scheduler;
		if (typeof maxConcurrency === "number" && maxConcurrency >= 1) config.scheduler.maxConcurrency = Math.floor(maxConcurrency);
		if (typeof maxDepth === "number" && maxDepth >= 0) config.scheduler.maxDepth = Math.floor(maxDepth);
	}
	if (Array.isArray(raw.harnessDenies)) {
		config.harnessDenies = (raw.harnessDenies as unknown[]).map((rule, index) => {
			if (!Array.isArray(rule) || rule.length !== 3 || rule[0] !== "deny") {
				throw new ConfigError(`${file}: harnessDenies[${index}] must be ["deny", action, resource]`);
			}
			return { effect: "deny", action: String(rule[1]), resource: String(rule[2]) };
		});
	}
	if (isRecord(raw.context)) {
		if (isRecord(raw.context.imageBudget)) Object.assign(config.context.imageBudget, raw.context.imageBudget);
		if (isRecord(raw.context.cacheAdvisory)) Object.assign(config.context.cacheAdvisory, raw.context.cacheAdvisory);
		if (typeof raw.context.projectInstructions === "boolean") config.context.projectInstructions = raw.context.projectInstructions;
	}
	if (typeof raw.supervisor === "string") config.supervisor = rel(base, raw.supervisor);
	return config;
}
