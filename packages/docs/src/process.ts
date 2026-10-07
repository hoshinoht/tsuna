export interface RunResult {
  code: number;
  stdout: string;
  stderr: string;
}

export interface RunOptions {
  cwd?: string;
  /** Extra variables merged over the inherited environment. */
  env?: Record<string, string>;
  signal?: AbortSignal;
}

export type Runner = (argv: string[], options?: RunOptions) => Promise<RunResult>;

export const spawnRunner: Runner = async (argv, options = {}) => {
  const [command, ...rest] = argv;
  if (!command) throw new Error("Empty command");
  let proc: ReturnType<typeof Bun.spawn>;
  try {
    proc = Bun.spawn([command, ...rest], {
      ...(options.cwd ? { cwd: options.cwd } : {}),
      env: { ...process.env, ...(options.env ?? {}) },
      stdin: "ignore",
      stdout: "pipe",
      stderr: "pipe",
      ...(options.signal ? { signal: options.signal } : {}),
    });
  } catch (error) {
    // Missing binaries surface as spawn errors (ENOENT); report them like a failed run.
    return { code: 127, stdout: "", stderr: `Could not run '${command}': ${error instanceof Error ? error.message : String(error)}` };
  }
  const [stdout, stderr, code] = await Promise.all([
    new Response(proc.stdout as ReadableStream).text(),
    new Response(proc.stderr as ReadableStream).text(),
    proc.exited,
  ]);
  return { code, stdout, stderr };
};

/** stderr when present, otherwise the last `lines` lines of stdout. */
export function failureText(result: RunResult, lines = 30): string {
  if (result.stderr.trim().length > 0) return result.stderr.trim();
  return result.stdout.trimEnd().split("\n").slice(-lines).join("\n");
}
