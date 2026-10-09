#!/usr/bin/env bun
/**
 * Measure the context footprint of each Tsuna agent:
 * - System prompt token count (Claude v5 & o200k_base tokenizers from pi-natives)
 * - Number of surfaced tools allowed by permission policies
 *
 * Usage:
 *   bun scripts/measure-context.ts [role]
 */
import { existsSync, readdirSync, readFileSync } from "node:fs";
import { join, resolve } from "node:path";
import { countTokens, Encoding } from "@oh-my-pi/pi-natives";
import { surfaceTools, type PermissionPolicies } from "../lib/permissions.ts";

const root = resolve(import.meta.dir, "..");
const rolesPath = join(root, "config/roles.json");
const permissionsPath = join(root, "config/permissions.json");
const promptsDir = join(root, "agent/prompts");

const roles = JSON.parse(readFileSync(rolesPath, "utf8")) as Record<string, { mode?: string; model?: string; description?: string }>;
const permissions = JSON.parse(readFileSync(permissionsPath, "utf8")) as PermissionPolicies;

const ALL_STANDARD_TOOLS = [
	"read", "edit", "write", "glob", "grep", "find", "ast_grep", "ast_edit",
	"apply_patch", "bash", "lsp", "debug", "eval", "task", "wait", "yield",
	"todo", "goal", "ask", "web_search", "manage_skill", "new_context",
];

const targetRole = process.argv[2];

interface RoleMeasurement {
	role: string;
	mode: string;
	toolsCount: number;
	surfacedTools: string[];
	claudeTokens: number;
	o200kTokens: number;
	chars: number;
	promptPreview: string;
}

const measurements: RoleMeasurement[] = [];

const promptFiles = readdirSync(promptsDir).filter(f => f.endsWith(".md"));
const roleNames = Object.keys(roles);

for (const role of roleNames) {
	if (targetRole && role !== targetRole) continue;

	const roleInfo = roles[role] ?? {};
	const promptFile = join(promptsDir, `${role}.md`);
	let promptContent = "";
	if (existsSync(promptFile)) {
		promptContent = readFileSync(promptFile, "utf8").trim();
	}

	const surfaced = surfaceTools(permissions, role, ALL_STANDARD_TOOLS);
	const claude = promptContent ? countTokens(promptContent, Encoding.ClaudeV5) : 0;
	const o200k = promptContent ? countTokens(promptContent, Encoding.O200kBase) : 0;

	measurements.push({
		role,
		mode: roleInfo.mode ?? "subagent",
		toolsCount: surfaced.length,
		surfacedTools: surfaced,
		claudeTokens: claude,
		o200kTokens: o200k,
		chars: promptContent.length,
		promptPreview: promptContent.slice(0, 120).replace(/\n/g, " "),
	});
}

// Print summary table
const headers = ["Agent Role", "Mode", "Tools", "Claude v5", "o200k", "Chars"];
const rows = measurements.map(m => [
	m.role,
	m.mode,
	String(m.toolsCount),
	String(m.claudeTokens),
	String(m.o200kTokens),
	String(m.chars),
]);

const allRows = [headers, ...rows];
const colWidths = headers.map((_, i) => Math.max(...allRows.map(r => r[i]!.length)));

console.log("\nTsuna Agent Context Measurement:");
console.log("=".repeat(colWidths.reduce((a, b) => a + b, 0) + (headers.length - 1) * 3));
console.log(headers.map((h, i) => h.padEnd(colWidths[i]!)).join(" | "));
console.log("-".repeat(colWidths.reduce((a, b) => a + b, 0) + (headers.length - 1) * 3));
for (const row of rows) {
	console.log(row.map((c, i) => c.padEnd(colWidths[i]!)).join(" | "));
}
console.log("=".repeat(colWidths.reduce((a, b) => a + b, 0) + (headers.length - 1) * 3));

if (targetRole && measurements.length === 1) {
	const m = measurements[0]!;
	console.log(`\nDetailed view for ${m.role}:`);
	console.log(`- Mode: ${m.mode}`);
	console.log(`- Surfaced tools (${m.toolsCount}): ${m.surfacedTools.join(", ")}`);
	console.log(`- Prompt preview: ${m.promptPreview}...`);
}
