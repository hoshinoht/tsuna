import { cpSync, mkdirSync, mkdtempSync, realpathSync, readdirSync, readFileSync, statSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

export const REPO_ROOT = resolve(dirname(fileURLToPath(import.meta.url)), "../../..");

let built: string | undefined;

/** The shiori binary under test: SHIORI_BIN, or a fresh CGO_ENABLED=0 build. */
export function shioriBin(): string {
  if (process.env.SHIORI_BIN) return process.env.SHIORI_BIN;
  if (built) return built;
  const out = join(mkdtempSync(join(tmpdir(), "shiori-bin-")), "shiori");
  const proc = Bun.spawnSync(["go", "build", "-trimpath", "-o", out, "./cmd/shiori"], {
    cwd: REPO_ROOT,
    env: { ...process.env, CGO_ENABLED: "0" },
    stderr: "pipe",
  });
  if (proc.exitCode !== 0) throw new Error(`go build failed: ${proc.stderr.toString()}`);
  built = out;
  return out;
}

export function tempRoot(prefix = "shiori-adapter-"): string {
  return realpathSync(mkdtempSync(join(tmpdir(), prefix)));
}

/** Copies a corpus fixture (testdata/fixtures/<name>) into a fresh root. */
export function fixtureRoot(name: string): string {
  const root = tempRoot(`shiori-${name}-`);
  cpSync(join(REPO_ROOT, "testdata", "fixtures", name), root, { recursive: true });
  return root;
}

export function seedPlan(root: string, id = "native-demo"): { jsonPath: string; markdownPath: string } {
  const directory = join(root, ".opencode", "workplan");
  mkdirSync(directory, { recursive: true });
  const now = "2026-09-30T00:00:00.000Z";
  const document = {
    schemaVersion: 2,
    id,
    kind: "software-engineering",
    title: "Native demo",
    goal: "Complete the current task",
    scope: [],
    nonGoals: [],
    constraints: [],
    relevantFiles: [],
    planFile: `.opencode/workplan/${id}.md`,
    specFiles: [],
    phases: [{
      id: "active",
      title: "Implementation",
      status: "in_progress",
      steps: [{ id: "next", title: "Do next thing", action: "Make the fix", validation: "Run the focused test", status: "in_progress" }],
    }],
    reviewFindings: [],
    notes: ["historical-note-must-not-be-returned"],
    status: "in_progress",
    createdAt: now,
    updatedAt: now,
  };
  const jsonPath = join(directory, `${id}.json`);
  const markdownPath = join(directory, `${id}.md`);
  writeFileSync(jsonPath, `${JSON.stringify(document, null, 2)}\n`);
  writeFileSync(markdownPath, "# Native demo\n\nA disposable native adapter fixture.\n");
  return { jsonPath, markdownPath };
}

export type Fingerprint = Map<string, string>;

/** Bytes, mode and mtime of every file (directories: existence only). */
export function fingerprint(root: string): Fingerprint {
  const out: Fingerprint = new Map();
  const walk = (dir: string) => {
    for (const name of readdirSync(dir)) {
      const p = join(dir, name);
      const st = statSync(p);
      if (st.isDirectory()) {
        out.set(p, "dir");
        walk(p);
      } else {
        out.set(p, `${st.mode}:${st.mtimeMs}:${readFileSync(p).toString("base64")}`);
      }
    }
  };
  walk(root);
  return out;
}

export function pause(ms: number): Promise<void> {
  return new Promise((r) => setTimeout(r, ms));
}
