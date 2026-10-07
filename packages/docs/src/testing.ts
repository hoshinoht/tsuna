// Shared fixtures for the docs plugin tests (not used at runtime).
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { makeRoots, type SearchRoots } from "./paths";
import type { RunOptions, RunResult, Runner } from "./process";

export interface Fixture {
  dir: string;
  roots: SearchRoots;
  write(path: string, content?: string): string;
  cleanup(): void;
}

/** Temp workspace with separate P (project), U (user) and B (bundled) roots. */
export function makeFixture(options: { realBundled?: boolean } = {}): Fixture {
  const dir = mkdtempSync(join(tmpdir(), "docs-plugin-test-"));
  const workspace = join(dir, "ws");
  mkdirSync(workspace, { recursive: true });
  const roots = makeRoots(workspace, {
    user: join(dir, "user"),
    ...(options.realBundled ? {} : { bundled: join(dir, "bundled") }),
  });
  return {
    dir,
    roots,
    write(path, content = "") {
      const full = path.startsWith("/") ? path : join(dir, path);
      mkdirSync(dirname(full), { recursive: true });
      writeFileSync(full, content);
      return full;
    },
    cleanup() {
      rmSync(dir, { recursive: true, force: true });
    },
  };
}

export interface RecordedCall {
  argv: string[];
  options: RunOptions;
}

export type StubHandler = (argv: string[], options: RunOptions) => Partial<RunResult> | void;

export function stubRunner(handler: StubHandler = () => {}): { run: Runner; calls: RecordedCall[] } {
  const calls: RecordedCall[] = [];
  const run: Runner = async (argv, options = {}) => {
    calls.push({ argv: [...argv], options: { ...options } });
    const result = handler(argv, options) ?? {};
    return { code: result.code ?? 0, stdout: result.stdout ?? "", stderr: result.stderr ?? "" };
  };
  return { run, calls };
}

/** Creates the `-o` target so success paths look real. */
export function touchOutput(argv: string[]): void {
  const index = argv.indexOf("-o");
  const out = index >= 0 ? argv[index + 1] : undefined;
  if (out) {
    mkdirSync(dirname(out), { recursive: true });
    writeFileSync(out, "%PDF-stub");
  }
}
