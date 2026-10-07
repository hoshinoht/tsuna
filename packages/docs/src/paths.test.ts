import { afterEach, describe, expect, test } from "bun:test";
import { existsSync, mkdirSync } from "node:fs";
import { join } from "node:path";
import {
  ensureUserConfigDirs,
  listCsl,
  listPresetFiles,
  listTemplates,
  resolveAsset,
  resolveCsl,
  resolvePreset,
  resolveTemplate,
  userPandocDir,
  USER_CONFIG_SUBDIRS,
} from "./paths";
import { makeFixture, type Fixture } from "./testing";

let fx: Fixture;
afterEach(() => fx?.cleanup());

describe("userPandocDir", () => {
  test("honours XDG_CONFIG_HOME and falls back to ~/.config", () => {
    expect(userPandocDir({ XDG_CONFIG_HOME: "/xdg" })).toBe("/xdg/opencode/pandoc");
    expect(userPandocDir({})).toEndWith("/.config/opencode/pandoc");
  });
});

describe("resolveTemplate", () => {
  test("searches project, then user, then bundled", () => {
    fx = makeFixture();
    const { roots } = fx;
    const bundled = fx.write(join(roots.bundled, "templates", "t", "template.latex"));
    expect(resolveTemplate(roots, "t")).toEqual({ path: bundled, source: "bundled" });
    const user = fx.write(join(roots.user, "templates", "t", "template.latex"));
    expect(resolveTemplate(roots, "t")).toEqual({ path: user, source: "user" });
    const project = fx.write(join(roots.project, "templates", "t", "template.latex"));
    expect(resolveTemplate(roots, "t")).toEqual({ path: project, source: "project" });
  });

  test(".latex suffix only when the name has no dot; exact files win", () => {
    fx = makeFixture();
    const { roots } = fx;
    const plain = fx.write(join(roots.user, "templates", "eisvogel.latex"));
    expect(resolveTemplate(roots, "eisvogel")?.path).toBe(plain);
    expect(resolveTemplate(roots, "eisvogel.latex")?.path).toBe(plain);
    fx.write(join(roots.user, "templates", "v1.2.latex"));
    expect(resolveTemplate(roots, "v1.2")).toBeUndefined();
    const nested = fx.write(join(roots.bundled, "templates", "sit-uofg", "template.latex"));
    expect(resolveTemplate(roots, "sit-uofg/template.latex")?.path).toBe(nested);
  });

  test("absolute names are returned only when they exist", () => {
    fx = makeFixture();
    const file = fx.write("abs/custom.latex");
    expect(resolveTemplate(fx.roots, file)).toEqual({ path: file, source: "user" });
    expect(resolveTemplate(fx.roots, join(fx.dir, "missing.latex"))).toBeUndefined();
  });
});

describe("resolvePreset / resolveCsl / resolveAsset", () => {
  test("preset lookup covers organizations/ and adds .yaml", () => {
    fx = makeFixture();
    const { roots } = fx;
    const org = fx.write(join(roots.user, "presets", "organizations", "acme.yaml"), "name: acme");
    expect(resolvePreset(roots, "acme")).toEqual({ path: org, source: "user" });
    expect(resolvePreset(roots, "acme.yaml")?.path).toBe(org);
    const project = fx.write(join(roots.project, "presets", "acme.yaml"), "name: acme");
    expect(resolvePreset(roots, "acme")).toEqual({ path: project, source: "project" });
    fx.write(join(roots.user, "presets", "deep", "hidden.yaml"));
    expect(resolvePreset(roots, "hidden")).toBeUndefined();
  });

  test("CSL lookup adds .csl and checks project then user only", () => {
    fx = makeFixture();
    const { roots } = fx;
    fx.write(join(roots.bundled, "csl", "apa.csl"));
    expect(resolveCsl(roots, "apa")).toBeUndefined();
    const user = fx.write(join(roots.user, "csl", "apa.csl"));
    expect(resolveCsl(roots, "apa")?.path).toBe(user);
    const project = fx.write(join(roots.project, "csl", "apa.csl"));
    expect(resolveCsl(roots, "apa.csl")).toEqual({ path: project, source: "project" });
  });

  test("asset lookup order is P, U, W, B", () => {
    fx = makeFixture();
    const { roots } = fx;
    const rel = "assets/logo.png";
    const b = fx.write(join(roots.bundled, rel));
    expect(resolveAsset(roots, rel)).toEqual({ path: b, source: "bundled" });
    const w = fx.write(join(roots.workspace, rel));
    expect(resolveAsset(roots, rel)).toEqual({ path: w, source: "project" });
    const u = fx.write(join(roots.user, rel));
    expect(resolveAsset(roots, rel)).toEqual({ path: u, source: "user" });
    const p = fx.write(join(roots.project, rel));
    expect(resolveAsset(roots, rel)).toEqual({ path: p, source: "project" });
  });
});

describe("listings", () => {
  test("templates dedupe across roots with the seen-before-check rule", () => {
    fx = makeFixture();
    const { roots } = fx;
    fx.write(join(roots.bundled, "templates", "alpha", "template.latex"));
    fx.write(join(roots.bundled, "templates", "beta.latex"));
    fx.write(join(roots.bundled, "templates", "gamma", "template.latex"));
    fx.write(join(roots.user, "templates", "beta.latex"));
    mkdirSync(join(roots.project, "templates", "gamma"), { recursive: true }); // no template.latex
    fx.write(join(roots.user, "templates", "notes.txt"));
    const listed = listTemplates(roots);
    // Scan order: user entries come before bundled ones; the empty project `gamma` dir hides
    // the real bundled gamma template (kept quirk).
    expect(listed.map((t) => [t.name, t.source])).toEqual([
      ["beta", "user"],
      ["alpha", "bundled"],
    ]);
    expect(listed.find((t) => t.name === "gamma")).toBeUndefined();
    expect(listed.find((t) => t.name === "notes.txt")).toBeUndefined();
  });

  test("preset files are found recursively, project first", () => {
    fx = makeFixture();
    const { roots } = fx;
    fx.write(join(roots.user, "presets", "a.yaml"));
    fx.write(join(roots.user, "presets", "deep", "nested", "b.yaml"));
    fx.write(join(roots.project, "presets", "a.yaml"));
    const listed = listPresetFiles(roots);
    expect(listed.map((p) => `${p.name}:${p.source}`).sort()).toEqual(["a:project", "b:user"]);
  });

  test("CSL listing is non-recursive and covers P and U only", () => {
    fx = makeFixture();
    const { roots } = fx;
    fx.write(join(roots.user, "csl", "ieee.csl"));
    fx.write(join(roots.user, "csl", "sub", "hidden.csl"));
    fx.write(join(roots.project, "csl", "ieee.csl"));
    fx.write(join(roots.bundled, "csl", "bundled.csl"));
    expect(listCsl(roots).map((c) => `${c.name}:${c.source}`)).toEqual(["ieee:project"]);
  });

  test("ensureUserConfigDirs creates every directory idempotently", () => {
    fx = makeFixture();
    ensureUserConfigDirs(fx.roots.user);
    ensureUserConfigDirs(fx.roots.user);
    for (const sub of USER_CONFIG_SUBDIRS) expect(existsSync(join(fx.roots.user, sub))).toBe(true);
    expect(existsSync(join(fx.roots.user, "templates", "ieee"))).toBe(false);
  });
});
