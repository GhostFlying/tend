/// <reference lib="dom" />
// Browser regression for the read-only directory. No real runner or model.
import assert from "node:assert/strict";
import { createServer, type ServerResponse } from "node:http";
import { mkdir, readFile } from "node:fs/promises";
import { resolve, join } from "node:path";
import { setTimeout as delay } from "node:timers/promises";
import { chromium } from "playwright";
import { issueView } from "./issue-fixture.ts";
import { fixtureRoot, navigate } from "./navigation.ts";
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
  title: (
    {
      implementation: "Implementation",
      "implementation-economy": "Economy implementation",
      investigation: "Investigation",
      "formal-review": "Formal review",
    } as Record<string, string>
  )[id!]!,
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
let activityRunning = false;
let delayActivityCatalog = false;
let prototypeActivity: "normal" | "idle" | "running" | "uncertain" = "normal";
let activityEventSeq = 2;
let secondHostOnline = true;
let failCatalog = false;
let nativeWorker = false;
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
    online: true,
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
    ["iris", "Iris", "investigation", "codex", "GPT-6.1-Sol", "medium"],
    ["maple", "Maple", "formal-review", "codex", "GPT-6.1-Sol", "xhigh"],
    ["birch", "Birch", "implementation-economy", "traex", "deepseek-v4.1-flash", "high"],
  ].map(([id, name, category, runtime, model, effort]) => ({
    identity: {
      execution_environment_id: host,
      local_id:
        prototypeActivity !== "normal" && host === "host-a" && id === "cedar" ? "constructor" : id!,
    },
    revision: 4,
    display_name:
      host === "host-a"
        ? name!
        : (
            { Cedar: "Pine", Iris: "Aster", Maple: "Elm", Birch: "Willow" } as Record<
              string,
              string
            >
          )[name!]!,
    runtime_binding_ref: { runtime_id: runtime!, revision: 1 },
    owner_runtime_binding_ref: { runtime_id: "native", revision: 1 },
    categories: [category!],
    purposes: ["worker_run"],
    requested_model: model!,
    requested_effort: effort!,
    enabled: true,
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
            identity: {
              execution_environment_id: host,
              local_id: prototypeActivity !== "normal" ? "toString" : "elm",
            },
            display_name: "Elm",
            revision: version,
            categories: ["implementation"],
            requested_model: "GPT-6.1-Sol",
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
                ? { ...runtime, revision: version - 1, cli: "traex" }
                : runtime,
            ),
          };
        })
      : [];
  return {
    task_activity: {
      running_tasks:
        prototypeActivity !== "normal" && host === "host-a"
          ? prototypeActivity === "running"
            ? { constructor: 2, toString: 1 }
            : {}
          : activityRunning
            ? host === "host-a"
              ? { cedar: 2, iris: 1, elm: 1 }
              : { birch: 3 }
            : {},
      uncertain_tasks:
        prototypeActivity !== "normal" && host === "host-a"
          ? prototypeActivity === "uncertain"
            ? { constructor: 1, toString: 1 }
            : {}
          : activityRunning && host === "host-a"
            ? { maple: 1 }
            : {},
    },
    host_id: host,
    online: host === "host-a" || secondHostOnline,
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
      runtimes: runtimes.map((runtime) =>
        nativeWorker && runtime.runtime_id === "codex"
          ? { ...runtime, kind: "appserver-worker" }
          : runtime,
      ),
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
const hostedRuntime: AgentInventoryRuntime = {
  ...runtimes[2]!,
  runtime_id: "traex",
  kind: "hosted-manager",
  cli: "codex",
};
const hostedAgents: AgentInventoryDefinition[] = [
  ["consultant", "Sage", "consultation"],
  ["manager", "Laurel", "issue_manager"],
  ["planner", "Sequoia", "main"],
].map(([id, name, purpose]) => ({
  identity: { execution_environment_id: "hosted", local_id: id! },
  revision: 2,
  display_name: name!,
  runtime_binding_ref: { runtime_id: "traex", revision: 1 },
  categories: ["planning"],
  purposes: [purpose!],
  requested_model: "GPT-6.1-Sol",
  requested_effort: "high",
  enabled: true,
  registration: "staged",
  availability: { available: false, reason: "isolation_not_verified" },
}));
categories.unshift({
  category_id: "planning",
  title: "Planning",
  description: "规划与协调",
  revision: 1,
  state: "active",
});
let hostedCLI: string | undefined = "codex";
let failAgentIssues = false;
const associationRequests: string[] = [];
const streams = new Set<ServerResponse>();
const writes: string[] = [];
let hideConcurrencyList = false;
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
  if (p === "/api/v1/concurrency-groups" && hideConcurrencyList)
    return json({ items: [], more: false });
  if (p.startsWith("/api/v1/concurrency-groups"))
    return json(
      p.includes("/concurrency-groups/")
        ? {
            id: "shared-codex",
            name: "共享 Codex",
            revision: 2,
            min: 1,
            initial: 4,
            max: 8,
            current: 4,
            occupied: 4,
            running: 4,
            waiting: 2,
            cooldown_until: 0,
            observation_since: Date.now() - 1800000,
            saturated_ms: 1080000,
            successful_turns: 12,
            feedback_healthy: true,
            bindings: [{ environment_id: "host-a", runtime_id: "codex", group_id: "shared-codex" }],
          }
        : {
            items: [
              {
                id: "shared-codex",
                name: "共享 Codex",
                revision: 2,
                min: 1,
                initial: 4,
                max: 8,
                current: 4,
                occupied: 4,
                running: 4,
                waiting: 2,
                cooldown_until: 0,
                observation_since: Date.now() - 1800000,
                saturated_ms: 1080000,
                successful_turns: 12,
                feedback_healthy: true,
                bindings: [
                  { environment_id: "host-a", runtime_id: "codex", group_id: "shared-codex" },
                ],
              },
            ],
            more: false,
          },
    );
  if (p === "/api/v1/concurrency-history")
    return json({
      since: Date.now() - 604800000,
      until: Date.now(),
      more: false,
      items: [
        {
          id: 2,
          group_id: "shared-codex",
          group_name: "共享 Codex",
          at: Date.now(),
          reason: "stable_demand",
          before: 3,
          after: 4,
          min: 1,
          initial: 4,
          max: 8,
          occupied: 3,
          cooldown_until: 0,
          window_start: Date.now() - 1800000,
          window_end: Date.now(),
          saturated_ms: 1080000,
          successful_turns: 12,
          congestion_count: 0,
          feedback_healthy: true,
          waiting: 2,
          sources: [],
        },
        {
          id: 1,
          group_id: "shared-codex",
          group_name: "共享 Codex",
          at: Date.now() - 3600000,
          reason: "congestion",
          before: 4,
          after: 3,
          min: 1,
          initial: 4,
          max: 8,
          occupied: 4,
          cooldown_until: 0,
          window_start: 0,
          window_end: 0,
          saturated_ms: 0,
          successful_turns: 0,
          congestion_count: 1,
          feedback_healthy: true,
          waiting: 2,
          sources: [
            {
              execution_id: "run:first",
              environment_id: "host-a",
              runtime_id: "codex",
              agent_id: "elm",
              issue_id: "issue-login",
              issue_title: "修复登录",
              task_id: "task-login",
              run_id: "first",
              session_id: "run:first",
              native_session_id: "native-login",
              turn_id: "turn-login",
              attempt: "1",
              kind: "congestion",
              code: "rate_limit_exceeded",
              http_status: 429,
              occurred_at: Date.now() - 3601000,
              received_at: Date.now() - 3600000,
              event_id: "fault-login",
            },
          ],
        },
      ],
    });
  if (p === "/api/v1/snapshot")
    return json({ event_seq: 1, tasks: [], hosts, projects: [], queue: [], decisions: [] });
  if (p === "/api/v1/categories") return json({ items: categories, more: false });
  if (p === "/api/v1/hosted-agents")
    return json({
      state: "registered_staged",
      configuration_revision: 2,
      task_activity: { running_tasks: {}, uncertain_tasks: {} },
      main_agent_id: "planner",
      projection: {
        format_version: 1,
        execution_environment_id: "hosted",
        source_kind: "inline",
        agents: hostedAgents,
        runtimes: [{ ...hostedRuntime, cli: hostedCLI }],
      },
    });
  if (/^\/api\/v1\/hosts\/[^/]+\/agents$/.test(p)) {
    if (failCatalog && p.includes("host-b")) {
      res.statusCode = 503;
      return json({ error: { code: "unavailable", message: "Temporary fixture failure" } });
    }
    const host = decodeURIComponent(p.split("/")[4]!);
    const response = catalog(host);
    // Hosts refresh independently; exercise a valid partial update before the
    // second host arrives instead of relying on matching response timing.
    if (delayActivityCatalog && host === "host-b") await delay(500);
    return json(response);
  }
  if (p === "/api/v1/agent-issues") {
    associationRequests.push(req.url!);
    if (failAgentIssues) {
      res.statusCode = 503;
      return json({
        error: { code: "unavailable", message: "Temporary association fixture failure" },
      });
    }
    const query = new URL(req.url!, "http://localhost").searchParams;
    const identity = {
      execution_environment_id: query.get("environment_id")!,
      local_id: query.get("local_id")!,
    };
    const history = query.get("scope") === "history";
    const second = !!query.get("after");
    const id = history
      ? second
        ? "wi_00000000000000000000000003"
        : "wi_00000000000000000000000002"
      : "wi_00000000000000000000000001";
    return json({
      identity,
      coverage: "recorded_run_pins",
      items:
        identity.local_id !== "cedar" || query.get("search") || query.get("state")
          ? []
          : [
              {
                issue_id: id,
                title: history ? (second ? "下一页历史关联" : "历史冻结关联") : "当前冻结关联",
                state: history ? "closed" : "active",
                current: !history,
                runs: (history ? ["finished"] : ["running", "unknown"]).map((observed, n) => ({
                  task_id: "task_00000000000000000000000001",
                  source: "run_responsibility",
                  pending_decision: n === 1,
                  task_open_questions: n === 1 ? 1 : 0,
                  run: {
                    run_id: `run_0000000000000000000000000${n + 1}`,
                    role: "implement",
                    generation: n + 1,
                    spec_version: 1,
                    host: identity.execution_environment_id,
                    observed,
                  },
                  pin: {
                    identity,
                    agent_revision: n + 1,
                    inventory_revision: n + 1,
                    snapshot_id: `original-snapshot-${n + 1}`,
                    public_sha256: hash,
                    runtime: { runtime_id: "original-worker", revision: 1 },
                    profile: { model: "original-model", effort: "high" },
                    worker: "appserver",
                    backend_id: "git",
                  },
                })),
              },
            ],
      more: history && !second,
      next: history && !second ? id : undefined,
    });
  }
  if (p === "/api/v1/issues/wi_00000000000000000000000001") {
    const issue = issueView();
    return json({
      ...issue,
      issue_id: "wi_00000000000000000000000001",
      plan: { ...issue.plan, title: "当前冻结关联" },
    });
  }
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
    { width: 1201, height: 900 },
    { width: 1024, height: 768 },
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
        if (e.type() === "error" && !((failCatalog || failAgentIssues) && e.text().includes("503")))
          errors.push(e.text());
      });
      hideConcurrencyList = false;
      secondHostOnline = true;
      activityRunning = false;
      delayActivityCatalog = false;
      prototypeActivity = "normal";
      nativeWorker = false;
      hostedCLI = "codex";
      associationRequests.length = 0;
      await page.goto(`${origin}/settings/agents?selected=hosted%2Fplanner`);
      await page.waitForURL((url) => url.pathname === "/agents");
      const directory = page.getByRole("region", { name: "Agents", exact: true });
      const mobileDetails = viewport.width <= 1399;
      const detail = () =>
        mobileDetails
          ? directory.locator('[class*="mobileDetails"]')
          : directory.getByRole("complementary");
      const backToList = async () => {
        if (mobileDetails)
          await directory.getByRole("button", { name: "返回列表", exact: true }).click();
      };
      await detail().getByRole("heading", { name: "Sequoia", exact: true }).waitFor();
      await detail().getByText("Codex · app-server", { exact: true }).waitFor();
      await detail()
        .getByText("此范围暂无已记录关联，不代表从未处理过 Issue。", { exact: true })
        .waitFor();
      assert.equal(
        associationRequests.length,
        1,
        "one selected Agent owns one initial association read",
      );
      assert.equal(
        await detail()
          .getByText("未接入运行观测", { exact: true })
          .filter({ visible: true })
          .count(),
        1,
      );
      if (mobileDetails) {
        await page.screenshot({
          path: join(output, `details-${viewport.width}-${theme}.png`),
          fullPage: false,
        });
        await backToList();
      }
      const names = directory.locator('[class*="identity"] strong').filter({ visible: true });
      const waitForOrder = async (order: string[]) => {
        const region = await directory.elementHandle();
        assert(region);
        try {
          await page.waitForFunction(
            ({ root, expected }) => {
              const visible = [...root.querySelectorAll('[class*="identity"] strong')].filter(
                (element) => element.getClientRects().length > 0,
              );
              return expected.every((name, index) => visible[index]?.textContent === name);
            },
            { root: region, expected: order },
          );
        } finally {
          await region.dispose();
        }
      };
      await names.first().waitFor();
      assert.equal(await names.count(), 11);
      assert.equal(await names.first().innerText(), "Sequoia");
      const typeSizes = await directory.evaluate((root) => {
        const px = (element: Element) => parseFloat(getComputedStyle(element).fontSize);
        const brand = document.querySelector('[class*="brand"] b');
        return {
          name: px(root.querySelector('[class*="identity"] strong')!),
          title: px(root.querySelector("h1")!),
          brand: brand ? px(brand) : 16,
        };
      });
      assert(
        typeSizes.name < typeSizes.title && typeSizes.title <= typeSizes.brand,
        JSON.stringify(typeSizes),
      );
      assert.equal(
        await directory.getByRole("button", { name: /新增|编辑|保存|reload/ }).count(),
        0,
      );
      assert.equal(await page.getByText("hosted/CLI 未记录", { exact: true }).count(), 0);
      assert.equal(
        await page.evaluate(() => document.documentElement.scrollWidth > innerWidth),
        false,
      );
      await page.screenshot({
        path: join(output, `agents-${viewport.width}-${theme}.png`),
        fullPage: false,
      });
      // Real browser + built UI + HTTP/SSE fixture: this proves display and
      // invalidation, not the backend execution transitions tested in App.
      const activityChanged = () => {
        const event = {
          event_seq: activityEventSeq++,
          object_type: "run",
          object_id: "fixture-run",
          kind: "run.observed",
          created_at: new Date().toISOString(),
        };
        for (const stream of streams)
          stream.write(`id: ${event.event_seq}\nevent: ledger\ndata: ${JSON.stringify(event)}\n\n`);
      };
      delayActivityCatalog = true;
      activityRunning = true;
      activityChanged();
      await waitForOrder(["Cedar", "Iris", "Elm", "Willow", "Maple"]);
      assert.deepEqual((await names.allInnerTexts()).slice(0, 5), [
        "Cedar",
        "Iris",
        "Elm",
        "Willow",
        "Maple",
      ]);
      assert.equal(await directory.getByRole("button", { name: /Elm.*1 个任务运行中/ }).count(), 1);
      const busyRow = directory.getByRole("button", { name: /Cedar.*2 个任务运行中/ });
      await busyRow.waitFor();
      assert.equal(
        await directory.getByText("运行状态待确认 · 1 个待确认", { exact: true }).count(),
        1,
      );
      assert.equal(
        await page.evaluate(() => document.documentElement.scrollWidth > innerWidth),
        false,
      );
      const activityBoxes = await directory
        .locator('[class*="identity"]')
        .filter({ visible: true })
        .evaluateAll((elements) =>
          elements.map((element) => {
            const name = element.querySelector("strong")!.getBoundingClientRect();
            const badge = element.querySelector('[class*="activity"]')!.getBoundingClientRect();
            return {
              overlap: name.bottom > badge.top + 1,
              width: element.scrollWidth > element.clientWidth + 1,
            };
          }),
        );
      assert(
        activityBoxes.every((value) => !value.overlap && !value.width),
        JSON.stringify(activityBoxes),
      );
      await page.screenshot({
        path: join(output, `busy-${viewport.width}-${theme}.png`),
        fullPage: false,
      });
      await directory
        .getByRole("combobox", { name: "类别筛选", exact: true })
        .selectOption("implementation");
      assert.deepEqual(await names.allInnerTexts(), ["Cedar", "Elm", "Pine"]);
      await busyRow.click();
      await detail().getByRole("heading", { name: "Cedar", exact: true }).waitFor();
      await backToList();
      await directory.getByRole("combobox", { name: "类别筛选", exact: true }).selectOption("");
      activityRunning = false;
      activityChanged();
      await waitForOrder(["Sequoia", "Laurel", "Sage", "Cedar", "Iris"]);
      await directory.getByRole("button", { name: /Willow.*0 个任务 · 空闲/ }).waitFor();
      assert.deepEqual((await names.allInnerTexts()).slice(0, 5), [
        "Sequoia",
        "Laurel",
        "Sage",
        "Cedar",
        "Iris",
      ]);
      await directory.getByRole("button", { name: /Cedar.*0 个任务 · 空闲/ }).waitFor();
      // Legal IDs can shadow Object.prototype after JSON decoding. Exercise
      // current and retired identities through the actual rendered directory.
      prototypeActivity = "idle";
      activityChanged();
      const waitForCedarID = async (id: string) =>
        page.waitForFunction(
          (expected) =>
            [...document.querySelectorAll('[class*="identity"]')].some(
              (element) =>
                element.querySelector("strong")?.textContent === "Cedar" &&
                element.querySelector("small")?.textContent?.includes(expected),
            ),
          id,
        );
      await waitForCedarID("constructor");
      assert.equal(await names.first().innerText(), "Sequoia");
      assert.equal(await names.count(), 11);
      assert.equal(await directory.getByText(/native code/).count(), 0);
      prototypeActivity = "running";
      activityChanged();
      await directory.getByRole("button", { name: /Cedar.*2 个任务运行中/ }).waitFor();
      await directory.getByRole("button", { name: /Elm.*1 个任务运行中/ }).waitFor();
      assert.deepEqual((await names.allInnerTexts()).slice(0, 2), ["Cedar", "Elm"]);
      assert.equal(await directory.getByRole("button", { name: /Elm.*1 个任务运行中/ }).count(), 1);
      await page.screenshot({
        path: join(output, `prototype-ids-${viewport.width}-${theme}.png`),
        fullPage: false,
      });
      prototypeActivity = "uncertain";
      activityChanged();
      await directory.getByRole("button", { name: /Cedar.*运行状态待确认 · 1 个待确认/ }).waitFor();
      await directory.getByRole("button", { name: /Elm.*运行状态待确认 · 1 个待确认/ }).waitFor();
      assert.equal(await directory.getByRole("button", { name: /Cedar.*任务运行中/ }).count(), 0);
      prototypeActivity = "normal";
      activityChanged();
      await waitForCedarID("cedar");
      await waitForOrder(["Sequoia", "Laurel", "Sage", "Cedar", "Iris"]);
      await directory.getByRole("button", { name: /Willow.*0 个任务 · 空闲/ }).waitFor();
      delayActivityCatalog = false;
      if (mobileDetails) {
        await page.setViewportSize({
          width: viewport.width,
          height: Math.min(viewport.height, 512),
        });
        const willow = directory.getByRole("button", { name: /Willow/ });
        await willow.scrollIntoViewIfNeeded();
        const before = await directory.evaluate((element) => element.scrollTop);
        assert(before > 0);
        await willow.click();
        await detail().getByRole("heading", { name: "Willow", exact: true }).waitFor();
        assert.equal(await directory.evaluate((element) => element.scrollTop), 0);
        failCatalog = true;
        await directory
          .getByRole("button", { name: "重新读取", exact: true })
          .filter({ visible: true })
          .click();
        await directory.getByRole("status").filter({ hasText: "部分目录暂时无法读取" }).waitFor();
        await detail().getByRole("heading", { name: "Willow", exact: true }).waitFor();
        failCatalog = false;
        await directory
          .getByRole("button", { name: "重新读取", exact: true })
          .filter({ visible: true })
          .click();
        await detail().getByRole("heading", { name: "Willow", exact: true }).waitFor();
        await directory
          .getByRole("status")
          .filter({ hasText: "部分目录暂时无法读取" })
          .waitFor({ state: "hidden" });
        await backToList();
        const after = await directory.evaluate((element) => element.scrollTop);
        assert(Math.abs(before - after) < 2, `list scroll changed from ${before} to ${after}`);
        assert.equal(await willow.evaluate((element) => document.activeElement === element), true);
        await page.setViewportSize(viewport);
        await directory.evaluate((element) => {
          element.scrollTop = 0;
        });
      }
      const filter = directory.getByRole("combobox", { name: "运行位置或方式筛选" });
      await filter.selectOption("location:host-b");
      assert.equal(await names.count(), 4);
      assert(await page.url().includes("location=host-b"));
      await page.reload();
      await names.first().waitFor();
      assert.equal(await names.count(), 4);
      await filter.selectOption("method:orca/traex");
      assert.equal(await names.count(), 2);
      await directory.getByRole("button", { name: /Birch birch|Birch.*Economy/ }).click();
      await detail().getByRole("heading", { name: "Birch", exact: true }).waitFor();
      await detail().getByText("绑定与版本", { exact: true }).click();
      await detail().getByText("TraeX · Orca · traex · v1", { exact: true }).waitFor();
      await detail().getByText("app-server", { exact: false }).first().waitFor();
      await backToList();
      await filter.selectOption("");
      await directory.getByRole("tab", { name: "Categories", exact: true }).click();
      await directory.getByRole("button", { name: "Cedar · host-a", exact: true }).waitFor();
      assert.equal(await directory.getByRole("button", { name: /elm · v/ }).count(), 0);
      await page.screenshot({
        path: join(output, `categories-${viewport.width}-${theme}.png`),
        fullPage: false,
      });
      await directory.getByRole("button", { name: "Cedar · host-a", exact: true }).click();
      await detail().getByRole("heading", { name: "Cedar", exact: true }).waitFor();
      const associations = detail().getByRole("region", { name: "关联 Issue", exact: true });
      await associations.getByRole("button", { name: "当前冻结关联", exact: true }).waitFor();
      await associations.getByText("实际执行（2）", { exact: true }).click();
      await associations.getByText("实施 · 结果未知，待对账", { exact: true }).waitFor();
      await associations.getByText("原执行仍有未结问题", { exact: true }).waitFor();
      assert.equal(await associations.getByText(/original-model/).count(), 2);
      const scope = associations.getByRole("combobox", { name: "Issue 关联范围" });
      await scope.selectOption("history");
      await associations.getByRole("button", { name: "历史冻结关联", exact: true }).waitFor();
      await associations.getByRole("button", { name: "下一页关联", exact: true }).click();
      await associations.getByRole("button", { name: "下一页历史关联", exact: true }).waitFor();
      const readsBeforeResize = associationRequests.length;
      await page.setViewportSize({
        width: viewport.width >= 1400 ? 1024 : 1536,
        height: viewport.height,
      });
      const resizedAssociations = page
        .getByRole("region", { name: "关联 Issue", exact: true })
        .filter({ visible: true });
      await resizedAssociations
        .getByRole("button", { name: "下一页历史关联", exact: true })
        .waitFor();
      assert.equal(
        await resizedAssociations.getByRole("combobox", { name: "Issue 关联范围" }).inputValue(),
        "history",
      );
      await page.waitForTimeout(100);
      assert.equal(
        associationRequests.length,
        readsBeforeResize,
        "responsive presentation must not reopen the shared reader",
      );
      await page.setViewportSize(viewport);
      await associations.getByRole("button", { name: "下一页历史关联", exact: true }).waitFor();
      await associations.getByRole("button", { name: "上一页关联", exact: true }).click();
      await associations.getByRole("button", { name: "历史冻结关联", exact: true }).waitFor();
      await associations.getByRole("textbox", { name: "搜索关联 Issue" }).fill("none");
      await associations
        .getByText("此范围暂无已记录关联，不代表从未处理过 Issue。", { exact: true })
        .waitFor();
      await associations.getByRole("textbox", { name: "搜索关联 Issue" }).fill("");
      await scope.selectOption("current");
      await associations.getByRole("button", { name: "当前冻结关联", exact: true }).waitFor();
      failAgentIssues = true;
      await associations.getByRole("button", { name: "刷新关联", exact: true }).click();
      await associations.getByRole("alert").waitFor();
      failAgentIssues = false;
      await associations.getByRole("button", { name: "刷新关联", exact: true }).click();
      await associations.getByRole("alert").waitFor({ state: "hidden" });
      await page.screenshot({
        path: join(output, `agent-issues-${viewport.width}-${theme}.png`),
        fullPage: false,
      });
      await associations.getByRole("button", { name: "当前冻结关联", exact: true }).click();
      await page.waitForURL("**/issues/wi_00000000000000000000000001");
      await page.getByRole("heading", { name: "当前冻结关联", exact: true }).waitFor();
      await navigate(page, "/agents?selected=host-a%2Fcedar");
      await detail().getByRole("heading", { name: "Cedar", exact: true }).waitFor();
      await backToList();
      await directory.getByRole("combobox", { name: "类别筛选", exact: true }).selectOption("");
      const historyRow = (version: number) =>
        directory.getByRole("button", { name: new RegExp(`Elm elm · v${version} · r${version}`) });
      assert.equal(await historyRow(2).isVisible(), false);
      await directory.getByText("历史定义（2）", { exact: true }).click();
      for (const version of [2, 3])
        assert(
          await historyRow(version)
            .getByText(`elm · v${version} · r${version}`, { exact: true })
            .isVisible(),
        );
      await historyRow(3).scrollIntoViewIfNeeded();
      await page.screenshot({
        path: join(output, `history-${viewport.width}-${theme}.png`),
        fullPage: false,
      });
      await historyRow(2).click();
      await detail().getByText("绑定与版本", { exact: true }).click();
      await detail().getByText("historical-v2", { exact: true }).waitFor();
      await detail().getByText("TraeX · Orca · codex · v1", { exact: true }).waitFor();
      await backToList();
      await historyRow(3).click();
      await detail().getByText("绑定与版本", { exact: true }).click();
      await detail().getByText("historical-v3", { exact: true }).waitFor();
      await detail().getByText("TraeX · Orca · codex · v2", { exact: true }).waitFor();
      await page.reload();
      await detail().getByRole("heading", { name: "Elm", exact: true }).waitFor();
      await detail().getByText("绑定与版本", { exact: true }).click();
      await detail().getByText("historical-v3", { exact: true }).waitFor();
      await backToList();
      secondHostOnline = false;
      hostedCLI = undefined;
      await directory
        .getByRole("button", { name: "重新读取", exact: true })
        .filter({ visible: true })
        .click();
      await directory
        .getByRole("button", { name: /Pine/ })
        .getByText("离线", { exact: true })
        .waitFor();
      await directory.getByRole("button", { name: /Sequoia/ }).click();
      await detail().getByText("app-server", { exact: true }).waitFor();
      await detail().getByText("绑定与版本", { exact: true }).click();
      await detail().getByText("未声明", { exact: true }).waitFor();
      assert.equal(
        await page.evaluate(() => document.documentElement.scrollWidth > innerWidth),
        false,
      );
      await backToList();
      hostedCLI = "codex";
      nativeWorker = true;
      await directory
        .getByRole("button", { name: "重新读取", exact: true })
        .filter({ visible: true })
        .click();
      await filter
        .getByRole("option", { name: "Codex · app-server（服务端）", exact: true })
        .waitFor({ state: "attached" });
      await filter
        .getByRole("option", { name: "Codex · app-server（主机）", exact: true })
        .waitFor({ state: "attached" });
      await filter.selectOption("method:hosted/codex");
      assert.equal(await names.count(), 3);
      await filter.selectOption("method:app-server/codex");
      assert.equal(await names.count(), 6);
      await directory.getByRole("tab", { name: "并发分组", exact: true }).click();
      const concurrency = page.getByRole("region", { name: "并发分组", exact: true });
      await concurrency.getByRole("button", { name: /共享 Codex/ }).click();
      await concurrency.getByRole("heading", { name: "共享 Codex", exact: true }).waitFor();
      assert(new URL(page.url()).searchParams.get("group") === "shared-codex");
      await concurrency.locator("summary").filter({ hasText: "拥塞退避" }).click();
      await concurrency.getByText("native-login", { exact: true }).waitFor();
      await concurrency.getByRole("button", { name: "修复登录", exact: true }).waitFor();
      await concurrency.locator("summary").filter({ hasText: "本组稳定满载" }).click();
      await concurrency.getByText(/满载累计 18 分钟/).waitFor();
      await concurrency.getByRole("button", { name: "重新读取", exact: true }).click();
      assert.equal(
        await page.evaluate(() => document.documentElement.scrollWidth > innerWidth),
        false,
      );
      await page.screenshot({
        path: join(output, `concurrency-${viewport.width}-${theme}.png`),
        fullPage: false,
      });
      hideConcurrencyList = true;
      await page.reload();
      await page.getByRole("heading", { name: "共享 Codex", exact: true }).waitFor();
      assert.equal(await concurrency.getByRole("button", { name: /共享 Codex/ }).count(), 0);
      await concurrency.getByText("18 分钟 / 至少 15 分钟", { exact: true }).waitFor();
      hideConcurrencyList = false;
      assert.deepEqual(errors, []);
      assert.deepEqual(writes, []);
      console.log(
        `PASS directory ${viewport.width} ${theme}: typography, declared hosted CLI, unknown CLI, location/method filters, historical pins, offline refresh, read-only, no overflow or console errors`,
      );
      await context.close();
    }
} finally {
  await browser.close();
  for (const stream of streams) stream.end();
  server.closeAllConnections();
  await new Promise<void>((done) => server.close(() => done()));
}
