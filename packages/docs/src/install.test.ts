import { afterEach, describe, expect, test } from "bun:test";
import { existsSync, readdirSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { CSL_BASE_URL, EISVOGEL_URL, INSTALL_SOURCES, type FetchLike } from "./install";
import { DocsService } from "./service";
import { makeFixture, stubRunner, type Fixture } from "./testing";

let fx: Fixture;
afterEach(() => fx?.cleanup());

function fakeFetch(responses: Record<string, { status: number; body?: string }>) {
  const urls: string[] = [];
  const fetch: FetchLike = async (url) => {
    urls.push(url);
    const response = responses[url] ?? { status: 404 };
    const bytes = new TextEncoder().encode(response.body ?? "");
    return { ok: response.status >= 200 && response.status < 300, status: response.status, arrayBuffer: async () => bytes.buffer as ArrayBuffer };
  };
  return { fetch, urls };
}

/** tar stub: "extracts" by writing eisvogel.latex into the -C directory unless told not to. */
function tarRunner(options: { code?: number; writeTemplate?: boolean } = {}) {
  return stubRunner((argv) => {
    if (argv[0] !== "tar") return;
    if (options.writeTemplate !== false) writeFileSync(join(argv[argv.indexOf("-C") + 1]!, "eisvogel.latex"), "% eisvogel");
    return { code: options.code ?? 0, stderr: options.code ? "tar: bad archive" : "" };
  });
}

describe("docs_templates_install", () => {
  test("offers eisvogel and the three CSL styles only", () => {
    expect([...INSTALL_SOURCES]).toEqual(["eisvogel", "csl-ieee", "csl-apa", "csl-acm"]);
  });

  test.each([
    ["csl-ieee", "ieee.csl"],
    ["csl-apa", "apa.csl"],
    ["csl-acm", "acm-sig-proceedings.csl"],
  ] as const)("%s downloads %s into U/csl", async (source, file) => {
    fx = makeFixture();
    const { fetch, urls } = fakeFetch({ [`${CSL_BASE_URL}${file}`]: { status: 200, body: "<style/>" } });
    const svc = new DocsService(fx.roots, { fetch });
    const dest = join(fx.roots.user, "csl", file);
    expect(await svc.templatesInstall({ source })).toBe(`Installed ${source} to ${dest}`);
    expect(urls).toEqual([`https://raw.githubusercontent.com/citation-style-language/styles/master/${file}`]);
    expect(readFileSync(dest, "utf8")).toBe("<style/>");
    for (const dir of ["templates", "csl", "presets", "presets/organizations", "assets"]) expect(existsSync(join(fx.roots.user, dir))).toBe(true);
  });

  test("non-ok HTTP and network errors are reported and nothing is written", async () => {
    fx = makeFixture();
    const { fetch } = fakeFetch({});
    const svc = new DocsService(fx.roots, { fetch });
    await expect(svc.templatesInstall({ source: "csl-apa" })).rejects.toThrow(/^HTTP 404$/);
    expect(existsSync(join(fx.roots.user, "csl", "apa.csl"))).toBe(false);
    const offline = new DocsService(fx.roots, {
      fetch: async () => {
        throw new Error("getaddrinfo ENOTFOUND");
      },
    });
    await expect(offline.templatesInstall({ source: "csl-apa" })).rejects.toThrow("Download failed: getaddrinfo ENOTFOUND");
    const empty = new DocsService(fx.roots, { fetch: fakeFetch({ [`${CSL_BASE_URL}apa.csl`]: { status: 200, body: "" } }).fetch });
    await expect(empty.templatesInstall({ source: "csl-apa" })).rejects.toThrow("Download failed: empty response body");
  });

  test("existing files are never overwritten without force", async () => {
    fx = makeFixture();
    const dest = fx.write(join(fx.roots.user, "csl", "ieee.csl"), "old");
    const { fetch } = fakeFetch({ [`${CSL_BASE_URL}ieee.csl`]: { status: 200, body: "new" } });
    const svc = new DocsService(fx.roots, { fetch });
    await expect(svc.templatesInstall({ source: "csl-ieee" })).rejects.toThrow(`Refusing to overwrite existing file ${dest}. Pass force: true to replace it.`);
    expect(readFileSync(dest, "utf8")).toBe("old");
    await svc.templatesInstall({ source: "csl-ieee", force: true });
    expect(readFileSync(dest, "utf8")).toBe("new");
  });

  test("eisvogel: download, extract, copy; shadowing the bundled copy needs force", async () => {
    fx = makeFixture();
    const bundled = fx.write(join(fx.roots.bundled, "templates", "eisvogel.latex"), "% bundled");
    const { fetch, urls } = fakeFetch({ [EISVOGEL_URL]: { status: 200, body: "tarball" } });
    const { run, calls } = tarRunner();
    const svc = new DocsService(fx.roots, { fetch, run, tmpdir: () => fx.dir });
    await expect(svc.templatesInstall({ source: "eisvogel" })).rejects.toThrow(`would shadow the bundled template ${bundled}`);
    expect(urls).toEqual([]);
    const dest = join(fx.roots.user, "templates", "eisvogel.latex");
    expect(await svc.templatesInstall({ source: "eisvogel", force: true })).toBe(`Installed eisvogel to ${dest}`);
    expect(urls).toEqual(["https://github.com/Wandmalfarbe/pandoc-latex-template/releases/download/v3.3.0/Eisvogel.tar.gz"]);
    expect(calls[0]!.argv.slice(0, 2)).toEqual(["tar", "-xzf"]);
    expect(readFileSync(dest, "utf8")).toBe("% eisvogel");
    // The temporary extraction directory is removed.
    expect(readdirSync(fx.dir).filter((f) => f.startsWith("eisvogel-install-"))).toEqual([]);
  });

  test("eisvogel failure modes: download, extraction, missing file", async () => {
    fx = makeFixture();
    const ok = fakeFetch({ [EISVOGEL_URL]: { status: 200, body: "tarball" } }).fetch;
    await expect(new DocsService(fx.roots, { fetch: fakeFetch({ [EISVOGEL_URL]: { status: 503 } }).fetch }).templatesInstall({ source: "eisvogel" })).rejects.toThrow(
      "Download failed: HTTP 503",
    );
    await expect(new DocsService(fx.roots, { fetch: ok, run: tarRunner({ code: 2 }).run, tmpdir: () => fx.dir }).templatesInstall({ source: "eisvogel" })).rejects.toThrow(
      "Extraction failed: tar: bad archive",
    );
    await expect(
      new DocsService(fx.roots, { fetch: ok, run: tarRunner({ writeTemplate: false }).run, tmpdir: () => fx.dir }).templatesInstall({ source: "eisvogel" }),
    ).rejects.toThrow("Extraction failed: eisvogel.latex not found at the archive root");
    expect(existsSync(join(fx.roots.user, "templates", "eisvogel.latex"))).toBe(false);
  });

  test("a successful install clears the preset cache", async () => {
    fx = makeFixture();
    const { fetch } = fakeFetch({ [`${CSL_BASE_URL}apa.csl`]: { status: 200, body: "<style/>" } });
    const svc = new DocsService(fx.roots, { fetch });
    svc.presets.load("eisvogel");
    expect(svc.presets.cacheSize).toBe(1);
    await svc.templatesInstall({ source: "csl-apa" });
    expect(svc.presets.cacheSize).toBe(0);
  });
});
