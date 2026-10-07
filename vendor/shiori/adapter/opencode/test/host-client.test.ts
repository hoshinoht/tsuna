import { afterAll, describe, expect, it } from "bun:test";
import { readdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { join } from "node:path";

import { BRIDGE_RPC_ID, discoverService, makeHostClient } from "../src/host-client";
import { tempRoot } from "./helpers";

const dirs: string[] = [];
afterAll(() => {
  for (const d of dirs) rmSync(d, { recursive: true, force: true });
});

type Seen = { method: string; path: string; search: string; auth: string | null; body: string };

function fakeHost(pid = 4242, version = "2.0.20") {
  const seen: Seen[] = [];
  const server = Bun.serve({
    port: 0,
    hostname: "127.0.0.1",
    async fetch(req) {
      const url = new URL(req.url);
      seen.push({ method: req.method, path: url.pathname, search: url.search, auth: req.headers.get("authorization"), body: req.method === "POST" ? await req.text() : "" });
      if (req.headers.get("authorization") !== "Basic " + Buffer.from("opencode:pw").toString("base64")) return new Response("no", { status: 401 });
      if (url.pathname === "/api/info") return Response.json({ version, pid });
      if (url.pathname.startsWith("/api/session/ses_1/permission")) return Response.json({ data: { id: "per_1", effect: "ask" } });
      if (url.pathname === "/api/session/ses_1") return Response.json({ data: { id: "ses_1", location: { directory: "/p" } } });
      if (url.pathname === `/api/rpc/${BRIDGE_RPC_ID}/prove`) return Response.json({ output: { challenge: "c", proof: "p" } });
      if (url.pathname === "/api/event") {
        const body = new ReadableStream({
          start(controller) {
            const enc = new TextEncoder();
            controller.enqueue(enc.encode('data: {"type":"server.connected","data":{}}\r\n\r\n'));
            controller.enqueue(enc.encode('event: x\ndata: {"type":"permission.asked",\ndata: "data":{"id":"per_1"},"location":{"directory":"/p"}}\n\n'));
            controller.enqueue(enc.encode(': keepalive\n\n'));
            controller.close();
          },
        });
        return new Response(body, { headers: { "content-type": "text/event-stream" } });
      }
      return new Response("missing", { status: 404 });
    },
  });
  return { server, seen, url: `http://127.0.0.1:${server.port}` };
}

describe("dependency-free public host client", () => {
  it("discovers only a healthy registered service and never starts one", async () => {
    const host = fakeHost();
    const dir = tempRoot("shiori-discover-");
    dirs.push(dir);
    const file = join(dir, "service.json");
    try {
      expect(await discoverService({ file })).toBeUndefined();
      writeFileSync(file, JSON.stringify({ url: host.url, pid: 4242, version: "2.0.20", password: "pw" }));
      expect(await discoverService({ file })).toEqual({ url: host.url, auth: { type: "basic", username: "opencode", password: "pw" } });
      writeFileSync(file, JSON.stringify({ url: host.url, pid: 1, password: "pw" }));
      expect(await discoverService({ file })).toBeUndefined(); // pid mismatch: not this registration
      writeFileSync(file, JSON.stringify({ url: host.url, pid: 4242, password: "wrong" }));
      expect(await discoverService({ file })).toBeUndefined(); // unauthenticated
      writeFileSync(file, JSON.stringify({ url: host.url, pid: 4242, version: "9.9.9", password: "pw" }));
      expect(await discoverService({ file })).toBeUndefined(); // version mismatch
    } finally {
      host.server.stop(true);
    }
  });

  it("speaks the public routes with exact bodies and parses the event stream", async () => {
    const host = fakeHost();
    try {
      const client = makeHostClient({ url: host.url, auth: { type: "basic", username: "opencode", password: "pw" } });
      expect(await client.server.info()).toEqual({ version: "2.0.20", pid: 4242 });
      expect(await client.session.get({ sessionID: "ses_1" })).toEqual({ id: "ses_1", location: { directory: "/p" } });
      expect(await client.permission.create({
        sessionID: "ses_1", action: "edit", resources: ["/p/a"], agent: "plan",
        source: { type: "tool", messageID: "m", id: "c" }, metadata: { workplanToolsBridge: { version: 1 } },
      })).toEqual({ id: "per_1", effect: "ask" });
      expect(await client.rpc.prove({ challenge: "c" }, { location: { directory: "/p q" } })).toEqual({ challenge: "c", proof: "p" });
      const events = [];
      for await (const e of client.event.subscribe()) events.push(e);
      expect(events.map((e) => e.type)).toEqual(["server.connected", "permission.asked"]);
      expect(events[1]).toEqual({ type: "permission.asked", data: { id: "per_1" }, location: { directory: "/p" } });

      const create = host.seen.find((s) => s.path === "/api/session/ses_1/permission")!;
      expect(JSON.parse(create.body)).toEqual({ action: "edit", resources: ["/p/a"], metadata: { workplanToolsBridge: { version: 1 } }, source: { type: "tool", messageID: "m", id: "c" }, agent: "plan" });
      const prove = host.seen.find((s) => s.path.endsWith("/prove"))!;
      expect(new URLSearchParams(prove.search).get("location[directory]")).toBe("/p q");
      expect(JSON.parse(prove.body)).toEqual({ input: { challenge: "c" } });
      await expect(makeHostClient({ url: host.url }).server.info()).rejects.toThrow(/UnexpectedStatus: 401/);
    } finally {
      host.server.stop(true);
    }
  });

  it("never reaches permission reply/rule APIs or starts a service (P06, static)", () => {
    const src = join(import.meta.dir, "..", "src");
    for (const name of readdirSync(src).filter((n) => n.endsWith(".ts"))) {
      // Code only: comments may name the forbidden APIs to explain why.
      const text = readFileSync(join(src, name), "utf8").replace(/\/\*[\s\S]*?\*\//g, "").replace(/^\s*\/\/.*$/gm, "");
      expect(text).not.toMatch(/\/reply|permission\/saved|\.reply\(|Service\.ensure|opencode serve|\bensure\(\{/);
      expect(text).not.toMatch(/from "@opencode\/client/);
    }
  });
});
