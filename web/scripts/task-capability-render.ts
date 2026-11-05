/// <reference lib="dom" />
// Isolated real React DOM regression; no Tend server, runner or model.
import assert from "node:assert/strict";
import { mkdir } from "node:fs/promises";
import { join } from "node:path";
import { build } from "vite";
import react from "@vitejs/plugin-react";
import { chromium } from "playwright";
import type { AgentCapabilityView, TaskCapabilityExplanation } from "../src/api/generated/api.ts";

const required = [{ capability_id: "image_gen", definition_revision: 1 }];
function candidate(id: string, revision: number, declared: boolean): AgentCapabilityView {
  const identity = { execution_environment_id: "linux-1", local_id: id };
  const runtime = { runtime_id: "worker", revision: 1 };
  return {
    identity,
    agent_revision: revision,
    runtime_binding_ref: runtime,
    categories: ["implementation"],
    purposes: ["worker_run"],
    declared: declared ? required : [],
    evidence: [],
    pin: {
      identity,
      agent_revision: revision,
      runtime,
      inventory_revision: revision,
      snapshot_id: `snapshot-${revision}`,
      public_sha256: `hash-${revision}`,
      profile: { model: "model", effort: "high" },
      worker: "orca",
      backend_id: "git",
      purpose: "worker_run",
    },
    current: {
      state: declared ? "eligible" : "unavailable",
      required,
      reasons: [
        {
          capability: required[0]!,
          state: declared ? "eligible" : "unavailable",
          reason: declared ? "capability_declared" : "capability_undeclared",
          recovery: "recheck",
        },
      ],
      evidence_ids: [],
      checked_at_ms: 1000,
    },
  };
}

const initial = [
  candidate("alpha", 2, false),
  candidate("alpha", 1, true),
  candidate("beta", 1, true),
];
const entry = "virtual:task-capability-render";
// Keep the production component, hooks, read state and refresh button intact.
// Only the API reader is a fixture; every read returns the next supplied facts.
const bundle = await build({
  configFile: false,
  define: { "process.env.NODE_ENV": JSON.stringify("development") },
  plugins: [
    react(),
    {
      name: entry,
      resolveId: (id) => (id === entry ? entry : undefined),
      load: (id) =>
        id === entry
          ? `
            import React from "react";
            import { createRoot } from "react-dom/client";
            import { TaskCapabilityFacts } from "${join(process.cwd(), "src/tasks/CapabilityFacts.tsx")}";
            import { ServicesContext } from "${join(process.cwd(), "src/app/services.tsx")}";
            const client = { get: async (path) => {
              window.readPaths.push(path);
              return structuredClone(window.facts);
            }};
            const task = { task_id: "task-capability", spec_version: 1,
              required_capabilities: { implementation: { required_capabilities: ${JSON.stringify(required)} } } };
            createRoot(document.getElementById("root")).render(
              React.createElement(ServicesContext.Provider, { value: { client } },
                React.createElement(TaskCapabilityFacts, { task })));
          `
          : undefined,
    },
  ],
  build: {
    write: false,
    minify: false,
    rolldownOptions: {
      input: entry,
      output: { format: "iife", name: "CapabilityRenderFixture" },
    },
  },
});
assert(!Array.isArray(bundle) && "output" in bundle);
const code = bundle.output.find((chunk) => chunk.type === "chunk" && chunk.isEntry);
assert(code?.type === "chunk");
const browser = await chromium.launch({ headless: true });
const output = process.env.TEND_LAYOUT_OUTPUT;
if (output) await mkdir(output, { recursive: true });
try {
  for (const width of [1280, 390]) {
    const page = await browser.newPage({ viewport: { width, height: 760 } });
    page.setDefaultTimeout(5000);
    const errors: string[] = [];
    page.on("pageerror", (error) => errors.push(error.message));
    page.on("console", (message) => {
      if (message.type() === "error" || message.type() === "warning") errors.push(message.text());
    });
    try {
      await page.setContent('<title>Task capability regression</title><main id="root"></main>');
      const facts: TaskCapabilityExplanation = {
        as_of: 1000,
        task_id: "task-capability",
        spec_version: 1,
        role: "implement",
        state: "eligible",
        required,
        checked_at_ms: 1000,
        deadline_ms: 601000,
        reasons: [],
        candidates: initial,
        recovery: "recheck",
      };
      await page.evaluate((value) => Object.assign(window, { facts: value, readPaths: [] }), facts);
      await page.addScriptTag({ content: code.code });
      assert.equal(await page.title(), "Task capability regression");
      await page.getByText("所需能力与匹配原因", { exact: true }).click();
      await page.waitForFunction(() => document.querySelectorAll("strong").length === 3);
      const details = await page.locator("details").elementHandle();
      if (output) await page.screenshot({ path: join(output, `capability-${width}-before.png`) });
      for (const candidates of [
        [initial[2]!],
        [initial[1]!, initial[2]!, initial[0]!],
        [initial[0]!, initial[2]!],
        [],
      ]) {
        await page.evaluate(
          (value) => {
            Object.assign(window, { facts: value });
          },
          { ...facts, candidates },
        );
        await page.getByRole("button", { name: "重新读取匹配", exact: true }).click();
        await page.waitForFunction(
          (count) => document.querySelectorAll("strong").length === count,
          candidates.length,
        );
        assert.deepEqual(
          await page.locator("strong").allTextContents(),
          candidates.map((c) => `linux-1/${c.identity.local_id}`),
        );
        const rows = await page.locator("details > div").allTextContents();
        assert.deepEqual(
          rows.map((row) => row.includes("capability_undeclared")),
          candidates.map((c) => c.current.state === "unavailable"),
        );
        assert(
          await details!.evaluate((node) => node === document.querySelector("details")),
          "refresh preserves the Task component",
        );
        if (output && candidates.length === 1)
          await page.screenshot({ path: join(output, `capability-${width}-after.png`) });
      }
      assert.deepEqual(
        await page.evaluate(() => (window as unknown as { readPaths: string[] }).readPaths),
        Array(5).fill(
          "/api/v1/tasks/task-capability/capability-match?role=implement&spec_version=1",
        ),
      );
      assert.deepEqual(errors, [], "no console warnings or render errors");
      console.log(
        `PASS ${width}px: current/retained -> beta -> reordered -> current only -> empty; no ghost rows`,
      );
    } catch (error) {
      console.error(await page.locator("body").innerText(), errors);
      if (output) await page.screenshot({ path: join(output, `capability-${width}-failure.png`) });
      throw error;
    } finally {
      await page.close();
    }
  }
} finally {
  await browser.close();
}
