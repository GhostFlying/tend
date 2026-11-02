/// <reference lib="dom" />
// Browser regression for the read-only directory. No real runner or model.
import assert from "node:assert/strict";
import { createServer, type ServerResponse } from "node:http";
import { mkdir, readFile } from "node:fs/promises";
import { resolve, join } from "node:path";
import { chromium } from "playwright";
import { fixtureRoot } from "./navigation.ts";
import type {
  AgentInventoryDefinition,
  AgentInventoryRuntime,
  RunnerAgentCatalog,
} from "../src/api/generated/api.ts";

const root = fixtureRoot();
const output = process.env.TEND_LAYOUT_OUTPUT ?? "/tmp/tend-agent-directory";
await mkdir(output, { recursive: true });
const hash = `sha256:${"a".repeat(64)}`;
const categories = [
  ["implementation", "复杂或存在不确定性的实现任务"],
  ["implementation-economy", "简单、确定、方案清楚的实现任务"],
  ["investigation", "源码调查、分析与复现"],
  ["formal-review", "独立正式评审与验收"],
].map(([id, description]) => ({
  category_id: id!,
  title: id!,
  description: description!,
  revision: 1,
  state: "active",
}));
const runtimes: AgentInventoryRuntime[] = [
  {
    runtime_id: "codex",
    revision: 1,
    kind: "orca-worker",
    cli: "codex",
    declared_purposes: ["worker_run"],
    enabled: true,
    registration: "staged",
    availability: { available: false, reason: "runtime_not_verified" },
  },
  {
    runtime_id: "traex",
    revision: 1,
    kind: "orca-worker",
    cli: "traex",
    declared_purposes: ["worker_run"],
    enabled: true,
    registration: "staged",
    availability: { available: false, reason: "runtime_not_verified" },
  },
  {
    runtime_id: "native",
    revision: 1,
    kind: "appserver-worker",
    cli: "codex",
    declared_purposes: ["issue_manager"],
    enabled: true,
    registration: "staged",
    availability: { available: false, reason: "runtime_not_verified" },
  },
];
const hosts = [
  {
    host_id: "host-a",
    online: true,
    status: "active",
    os: "linux",
    slots: 4,
    labels: [],
    repos: [],
  },
  {
    host_id: "host-b",
    online: false,
    status: "active",
    os: "linux",
    slots: 4,
    labels: [],
    repos: [],
  },
];
function catalog(host: string): RunnerAgentCatalog {
  const agents: AgentInventoryDefinition[] = [
    ["cedar", "Cedar", "implementation", "codex", "GPT-6.1-Sol", "high"],
    ["birch", "Birch", "implementation-economy", "traex", "DeepSeek V4.1 Flash", "high"],
    ["iris", "Iris", "investigation", "codex", "GPT-6.1-Sol", "medium"],
    ["maple", "Maple", "formal-review", "codex", "GPT-6.1-Sol", "xhigh"],
  ]
    .filter((_, i) => host === "host-a" || i === 0)
    .map(([id, name, category, runtime, model, effort]) => ({
      identity: { execution_environment_id: host, local_id: id! },
      revision: 4,
      display_name: name!,
      runtime_binding_ref: { runtime_id: runtime!, revision: 1 },
      owner_runtime_binding_ref: { runtime_id: "native", revision: 1 },
      categories: [category!],
      purposes: ["worker_run"],
      requested_model: model!,
      requested_effort: effort!,
      enabled: id !== "maple",
      registration: "staged",
      availability: {
        available: false,
        reason: id === "maple" ? "definition_disabled" : "runtime_not_verified",
      },
    }));
  const histories =
    host === "host-a"
      ? [2, 3].map((version) => {
          const historical: AgentInventoryDefinition = {
            ...agents[0]!,
            identity: { execution_environment_id: host, local_id: "elm" },
            display_name: `Elm v${version}`,
            revision: version,
            categories: [version === 2 ? "implementation" : "investigation"],
            requested_model: version === 2 ? "GPT-6.1-Sol" : "DeepSeek V4.1 Flash",
            runtime_binding_ref: { runtime_id: "codex", revision: version - 1 },
          };
          return {
            definition: historical,
            pin: {
              identity: historical.identity,
              agent_revision: version,
              runtime: historical.runtime_binding_ref,
              inventory_revision: version,
              snapshot_id: `historical-v${version}`,
              public_sha256: `sha256:${String(version).repeat(64)}`,
              profile: { model: historical.requested_model, effort: historical.requested_effort },
              worker: "orca" as const,
              backend_id: "orca",
            },
            runtimes: runtimes.map((runtime) =>
              runtime.runtime_id === "codex"
                ? { ...runtime, revision: version - 1, cli: version === 2 ? "codex" : "traex" }
                : runtime,
            ),
          };
        })
      : [];
  return {
    host_id: host,
    online: host === "host-a",
    continuation_supported: true,
    last_heartbeat_at_ms: 1793586720000,
    current: {
      host_id: host,
      server_revision: 4,
      local_revision: 4,
      snapshot_id: "current",
      public_sha256: hash,
      request_id: "registration",
      state: "registered_staged",
      categories,
    },
    projection: {
      format_version: 2,
      execution_environment_id: host,
      source_kind: "local_file",
      runtimes,
      agents,
    },
    executors: agents.flatMap((a) =>
      ["", "issue_manager"].map((purpose) => ({
        available: a.enabled,
        pin: {
          identity: a.identity,
          agent_revision: 4,
          runtime: purpose ? a.owner_runtime_binding_ref! : a.runtime_binding_ref,
          inventory_revision: 4,
          snapshot_id: "current",
          public_sha256: hash,
          profile: { model: a.requested_model, effort: a.requested_effort },
          purpose,
          worker: purpose ? ("appserver" as const) : ("orca" as const),
          backend_id: purpose ? "" : "orca",
        },
      })),
    ),
    retained_definitions: histories,
    retained_executions: histories.map((history) => history.pin),
    continuations: histories.map((history, index) => ({
      pin: history.pin,
      available: index === 0,
    })),
  };
}
const streams = new Set<ServerResponse>();
const writes: string[] = [];
const server = createServer(async (req, res) => {
  const u = new URL(req.url!, "http://fixture.invalid"),
    p = u.pathname;
  const json = (value: unknown) => {
    res.setHeader("content-type", "application/json");
    res.end(JSON.stringify(value));
  };
  if (req.method !== "GET" && p !== "/api/v1/session") {
    writes.push(p);
    res.statusCode = 405;
    return json({});
  }
  if (p.endsWith("/events")) {
    res.writeHead(200, { "content-type": "text/event-stream" });
    res.write('event: start\ndata: {"event_seq":1}\n\n');
    streams.add(res);
    res.on("close", () => streams.delete(res));
    return;
  }
  if (p === "/api/v1/session")
    return json({
      token: "fixture",
      login: "owner",
      scope: "owner",
      client_id: "directory",
      deployment_id: "directory",
    });
  if (p === "/api/v1/snapshot")
    return json({ event_seq: 1, tasks: [], hosts, projects: [], queue: [], decisions: [] });
  if (p === "/api/v1/categories") return json({ items: categories, more: false });
  if (p === "/api/v1/hosted-agents")
    return json({
      state: "unconfigured",
      projection: {
        format_version: 1,
        execution_environment_id: "",
        source_kind: "inline",
        agents: [],
        runtimes: [],
      },
    });
  if (/^\/api\/v1\/hosts\/[^/]+\/agents$/.test(p))
    return json(catalog(decodeURIComponent(p.split("/")[4]!)));
  if (p === "/api/v1/messaging/messages") return json({ items: [], next: 0, more: false });
  if (p === "/api/v1/attention") return json({ items: [], more: false });
  if (p.startsWith("/api/"))
    return json({ items: [], more: false, revision: 1, enabled: false, available: false });
  try {
    const file = resolve(root, p.split("/").at(-1)?.includes(".") ? `.${p}` : "index.html");
    assert(file.startsWith(root + "/"));
    res.setHeader(
      "content-type",
      p.endsWith(".js")
        ? "application/javascript"
        : p.endsWith(".css")
          ? "text/css"
          : p.endsWith(".json")
            ? "application/json"
            : p.endsWith(".png")
              ? "image/png"
              : "text/html",
    );
    res.end(await readFile(file));
  } catch {
    res.statusCode = 404;
    res.end();
  }
});
await new Promise<void>((done) => server.listen(0, "127.0.0.1", done));
const address = server.address();
assert(address && typeof address !== "string");
const origin = `http://127.0.0.1:${address.port}`;
const browser = await chromium.launch();
try {
  for (const viewport of [
    { width: 1536, height: 1024 },
    { width: 390, height: 844 },
    { width: 320, height: 640 },
  ])
    for (const theme of ["light", "dark"] as const) {
      const context = await browser.newContext({
        viewport,
        colorScheme: theme,
        serviceWorkers: "block",
      });
      await context.addInitScript((value) => localStorage.setItem("tend.theme", value), theme);
      const page = await context.newPage();
      const errors: string[] = [];
      page.on("pageerror", (e) => errors.push(e.message));
      page.on("console", (e) => {
        if (e.type() === "error") errors.push(e.text());
      });
      await page.goto(`${origin}/settings/agents?selected=host-a%2Fcedar`);
      await page.waitForURL((url) => url.pathname === "/agents");
      await page.getByRole("heading", { name: "Agents", exact: true }).waitFor();
      const directory = page.getByRole("region", { name: "Agents", exact: true });
      if (viewport.width <= 1200)
        await directory.getByText("完整定义与观测", { exact: true }).click();
      await directory.getByText("slug：cedar", { exact: true }).filter({ visible: true }).waitFor();
      await directory
        .getByText("orca/traex", { exact: true })
        .filter({ visible: true })
        .first()
        .waitFor();
      assert.equal(
        await directory.getByRole("button", { name: /新增|编辑|保存|reload/ }).count(),
        0,
      );
      assert.equal(
        await page.evaluate(() => document.documentElement.scrollWidth > innerWidth),
        false,
      );
      if (viewport.width <= 1200)
        await directory.getByText("完整定义与观测", { exact: true }).click();
      await page.screenshot({
        path: join(output, `agents-${viewport.width}-${theme}.png`),
        fullPage: true,
      });
      await directory
        .getByRole("combobox", { name: "运行环境筛选", exact: true })
        .selectOption("orca/traex");
      assert.equal(await directory.getByRole("button", { name: /Cedar cedar/ }).count(), 0);
      await directory.getByRole("button", { name: /Birch birch/ }).click();
      if (viewport.width <= 1200)
        await directory.getByText("完整定义与观测", { exact: true }).click();
      await directory.getByText("slug：birch", { exact: true }).filter({ visible: true }).waitFor();
      await page.reload();
      if (viewport.width <= 1200)
        await directory.getByText("完整定义与观测", { exact: true }).click();
      await directory.getByText("slug：birch", { exact: true }).filter({ visible: true }).waitFor();
      await directory.getByRole("tab", { name: "Categories", exact: true }).click();
      await directory.getByRole("button", { name: "Cedar · host-a", exact: true }).waitFor();
      await page.screenshot({
        path: join(output, `categories-${viewport.width}-${theme}.png`),
        fullPage: true,
      });
      await directory.getByRole("button", { name: "Cedar · host-a", exact: true }).click();
      if (viewport.width <= 1200)
        await directory.getByText("完整定义与观测", { exact: true }).click();
      await directory.getByText("slug：cedar", { exact: true }).filter({ visible: true }).waitFor();
      await directory
        .getByText("orca/codex · codex · v1", { exact: true })
        .filter({ visible: true })
        .waitFor();
      await directory
        .getByText("app-server/codex · native · v1", { exact: true })
        .filter({ visible: true })
        .waitFor();
      await directory.getByRole("combobox", { name: "类别筛选", exact: true }).selectOption("");
      await directory.getByRole("button", { name: /Elm v2 elm/ }).click();
      if (viewport.width <= 1200)
        await directory.getByText("完整定义与观测", { exact: true }).click();
      await directory
        .getByText("历史登记 · 仅续接旧责任", { exact: true })
        .filter({ visible: true })
        .first()
        .waitFor();
      await directory
        .getByText("historical-v2", { exact: true })
        .filter({ visible: true })
        .waitFor();
      await directory
        .getByText("orca/codex · codex · v1", { exact: true })
        .filter({ visible: true })
        .waitFor();
      await directory.getByRole("button", { name: /Elm v3 elm/ }).click();
      if (viewport.width <= 1200)
        await directory.getByText("完整定义与观测", { exact: true }).click();
      await directory
        .getByText("historical-v3", { exact: true })
        .filter({ visible: true })
        .waitFor();
      await directory
        .getByText("orca/traex · codex · v2", { exact: true })
        .filter({ visible: true })
        .waitFor();
      await page.reload();
      if (viewport.width <= 1200)
        await directory.getByText("完整定义与观测", { exact: true }).click();
      await directory
        .getByText("historical-v3", { exact: true })
        .filter({ visible: true })
        .waitFor();
      assert.deepEqual(errors, []);
      assert.deepEqual(writes, []);
      console.log(
        `PASS directory ${viewport.width} ${theme}: legacy link, exact environment filter, reload, category links, read-only, no overflow or console errors`,
      );
      await context.close();
    }
} finally {
  await browser.close();
  for (const stream of streams) stream.end();
  server.closeAllConnections();
  await new Promise<void>((done) => server.close(() => done()));
}
