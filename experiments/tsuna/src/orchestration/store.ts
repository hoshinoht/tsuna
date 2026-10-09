/**
 * Durable agent-tree state for one root session.
 *
 * Layout: <state>/agents/<rootId>/
 *   lock                      owner pid + kernel start identity
 *   records/<agentId>.json    one AgentRecord per agent, written atomically
 *
 * Transcripts are Pi JSONL files under <state>/sessions/<rootId>/<agentId>/.
 * One Tsuna process owns a root at a time; a live owner blocks a second open.
 */
import { createHash, randomBytes } from "node:crypto";
import { closeSync, existsSync, fsyncSync, linkSync, mkdirSync, openSync, readdirSync, readFileSync, renameSync, rmSync, writeFileSync, writeSync } from "node:fs";
import { join } from "node:path";
import type { AgentContract, AgentRecord } from "./types.ts";

export class StoreError extends Error {}

/**
 * Integrity hash over the contract *and* the agent's identity/position in the
 * tree (id, root, parent, depth), so a corrupted or edited record cannot
 * silently change its authority. Integrity only: see DESIGN D10.
 */
export function contractHash(contract: AgentContract, identity: { id: string; rootId: string; parentId: string | null; depth: number }): string {
	return createHash("sha256").update(stableStringify({ contract, identity: { id: identity.id, rootId: identity.rootId, parentId: identity.parentId, depth: identity.depth } })).digest("hex");
}

export function stableStringify(value: unknown): string {
	if (Array.isArray(value)) return `[${value.map(stableStringify).join(",")}]`;
	if (value && typeof value === "object") {
		const entries = Object.entries(value as Record<string, unknown>)
			.filter(([, v]) => v !== undefined)
			.sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0));
		return `{${entries.map(([k, v]) => `${JSON.stringify(k)}:${stableStringify(v)}`).join(",")}}`;
	}
	return JSON.stringify(value);
}

export function writeJsonAtomic(path: string, value: unknown): void {
	const tmp = `${path}.${process.pid}.${randomBytes(6).toString("hex")}.tmp`;
	const fd = openSync(tmp, "wx", 0o600);
	try {
		writeSync(fd, JSON.stringify(value, null, 1));
		fsyncSync(fd);
	} finally {
		closeSync(fd);
	}
	renameSync(tmp, path);
}

/** Linux/macOS process birth identity so a reused PID is not mistaken for the owner. */
export function processIdentity(pid: number): string | undefined {
	try {
		const stat = readFileSync(`/proc/${pid}/stat`, "utf8");
		const rest = stat.slice(stat.lastIndexOf(") ") + 2).split(" ");
		if (rest[0] === "Z") return undefined;
		return `linux:${rest[19]}`;
	} catch {}
	if (process.platform === "darwin") {
		try {
			const out = Bun.spawnSync(["ps", "-o", "lstart=", "-p", String(pid)]).stdout.toString().trim();
			return out ? `mac:${out}` : undefined;
		} catch {}
	}
	return undefined;
}

/** Roots owned by runtimes in this process (the pid check cannot tell them apart). */
const HELD_IN_PROCESS = new Set<string>();

export class AgentStore {
	readonly dir: string;
	readonly recordsDir: string;
	private lockHeld = false;

	constructor(stateAgentsDir: string, readonly rootId: string) {
		if (!/^[A-Za-z0-9_-]{1,64}$/.test(rootId)) throw new StoreError(`invalid root id ${rootId}`);
		this.dir = join(stateAgentsDir, rootId);
		this.recordsDir = join(this.dir, "records");
	}

	exists(): boolean {
		return existsSync(this.recordsDir);
	}

	/** Claim single-process ownership of this root. */
	lock(): void {
		if (HELD_IN_PROCESS.has(this.dir)) throw new StoreError(`root ${this.rootId} is owned by another runtime in this process`);
		mkdirSync(this.recordsDir, { recursive: true, mode: 0o700 });
		const path = join(this.dir, "lock");
		const mine = { pid: process.pid, identity: processIdentity(process.pid) ?? "unknown" };
		for (let attempt = 0; attempt < 2; attempt++) {
			// Write the owner record to a private temp file, then hard-link it into
			// place: the lock never exists without its owner (no empty-file window).
			const tmp = `${path}.${process.pid}.${randomBytes(6).toString("hex")}`;
			writeFileSync(tmp, JSON.stringify(mine), { mode: 0o600, flag: "wx" });
			try {
				linkSync(tmp, path);
				this.lockHeld = true;
				HELD_IN_PROCESS.add(this.dir);
				return;
			} catch (error) {
				if ((error as NodeJS.ErrnoException).code !== "EEXIST") throw error;
				let owner: { pid?: number; identity?: string } = {};
				try {
					owner = JSON.parse(readFileSync(path, "utf8"));
				} catch {}
				const live = typeof owner.pid === "number" && processIdentity(owner.pid) === owner.identity;
				if (live) throw new StoreError(`root ${this.rootId} is owned by live process ${owner.pid}`);
				// Stale owner: remove only if the file still holds that owner's record.
				try {
					if (readFileSync(path, "utf8") === JSON.stringify(owner)) rmSync(path, { force: true });
				} catch {}
			} finally {
				rmSync(tmp, { force: true });
			}
		}
		throw new StoreError(`could not lock root ${this.rootId}`);
	}

	unlock(): void {
		if (!this.lockHeld) return;
		rmSync(join(this.dir, "lock"), { force: true });
		this.lockHeld = false;
		HELD_IN_PROCESS.delete(this.dir);
	}

	save(record: AgentRecord): void {
		record.updatedAt = Date.now();
		writeJsonAtomic(join(this.recordsDir, `${record.id}.json`), record);
	}

	/** Load every record; corrupt files are reported, never guessed. */
	loadAll(): { records: AgentRecord[]; corrupt: { file: string; error: string }[] } {
		const records: AgentRecord[] = [];
		const corrupt: { file: string; error: string }[] = [];
		if (!existsSync(this.recordsDir)) return { records, corrupt };
		for (const entry of readdirSync(this.recordsDir)) {
			if (!entry.endsWith(".json")) continue;
			const file = join(this.recordsDir, entry);
			try {
				const record = JSON.parse(readFileSync(file, "utf8")) as AgentRecord;
				if (record.version !== 1 || typeof record.id !== "string" || record.rootId !== this.rootId || !record.contract) {
					throw new Error("not a Tsuna agent record for this root");
				}
				if (`${record.id}.json` !== entry) throw new Error("record id does not match file name");
				records.push(record);
			} catch (error) {
				corrupt.push({ file, error: (error as Error).message });
			}
		}
		return { records, corrupt };
	}
}
