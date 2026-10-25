import { expect, test } from "vitest";
import type { TaskDetail } from "../api/generated/api.ts";
import { currentWork } from "./facts.ts";
function detail(): TaskDetail {
  return {
    task_id: "task",
    title: "test",
    lifecycle: "changes_required",
    project: "demo",
    rating: "",
    control_mode: "managed",
    control_revision: 1,
    spec_version: 2,
    next_implement_generation: 3,
    block_reason: "none",
    workspace: "",
    spec: {},
    decisions: [],
    actions: [
      { action_id: "old", kind: "implement", status: "done", generation: 1, spec_version: 1 },
      { action_id: "current", kind: "implement", status: "done", generation: 2, spec_version: 2 },
    ],
    runs: [
      {
        run_id: "run1",
        spec_version: 1,
        role: "implement",
        host: "host",
        generation: 1,
        observed: "finished",
      },
      {
        run_id: "run2",
        spec_version: 2,
        role: "implement",
        host: "host",
        generation: 2,
        observed: "finished",
      },
    ],
    artifacts: [
      { artifact_id: "old", run_id: "run1", ref: {} },
      { artifact_id: "current", run_id: "run2", ref: {} },
    ],
    acceptances: [
      {
        artifact_id: "old",
        acceptance_id: "acc_fixture",
        spec_version: 1,
        verdict: "changes_required",
        issues: ["historical failure"],
      },
      {
        artifact_id: "current",
        acceptance_id: "acc_fixture",
        spec_version: 2,
        verdict: "changes_required",
        issues: ["current issue"],
      },
    ],
  };
}
test("current issues and artifacts are tied to spec and implementation identity", () => {
  const work = currentWork(detail());
  expect(work.artifacts.map((a) => a.artifact_id)).toEqual(["current"]);
  expect(work.issues.map((v) => v.issues)).toEqual([["current issue"]]);
});
test("a passed reacceptance replaces the old failure and active implementation does not surface history", () => {
  const t = detail();
  t.acceptances!.push({
    artifact_id: "current",
    acceptance_id: "acc_fixture",
    spec_version: 2,
    verdict: "passed",
    issues: [],
  });
  expect(currentWork(t).issues).toEqual([]);
  t.lifecycle = "running";
  t.acceptances!.pop();
  expect(currentWork(t).issues).toEqual([]);
});
test("the newest implementation artifact wins over a failed earlier artifact on the same spec", () => {
  const t = detail();
  t.actions!.push({
    action_id: "new",
    kind: "implement",
    status: "done",
    generation: 3,
    spec_version: 2,
  });
  t.runs!.push({
    run_id: "run3",
    spec_version: 2,
    role: "implement",
    host: "host",
    generation: 3,
    observed: "finished",
  });
  t.artifacts!.push({ artifact_id: "new", run_id: "run3", ref: {} });
  expect(currentWork(t).artifacts.map((a) => a.artifact_id)).toEqual(["new"]);
  expect(currentWork(t).issues).toEqual([]);
});

test("implementation generations reused across specs never associate an old artifact", () => {
  const t = detail();
  t.actions = [
    { action_id: "old", kind: "implement", status: "done", generation: 1, spec_version: 1 },
    { action_id: "current", kind: "implement", status: "ready", generation: 1, spec_version: 2 },
  ];
  t.runs = [
    {
      run_id: "old",
      role: "implement",
      generation: 1,
      spec_version: 1,
      host: "host",
      observed: "finished",
    },
  ];
  t.artifacts = [{ artifact_id: "old", run_id: "old", ref: {} }];
  t.acceptances = [
    {
      artifact_id: "old",
      acceptance_id: "acc_fixture",
      spec_version: 1,
      verdict: "passed",
      issues: [],
    },
  ];
  expect(currentWork(t).artifacts).toEqual([]);
  t.runs.push({
    run_id: "new",
    role: "implement",
    generation: 1,
    spec_version: 2,
    host: "host",
    observed: "finished",
  });
  t.artifacts.push({ artifact_id: "new", run_id: "new", ref: {} });
  expect(currentWork(t).artifacts.map((a) => a.artifact_id)).toEqual(["new"]);
});

test("the authoritative artifact beats unrelated implementation and acceptance generations", () => {
  const t = detail();
  t.current_artifact_id = "old";
  t.runs![0]!.generation = 8;
  t.actions!.push({
    action_id: "accept-current",
    kind: "accept",
    generation: 1,
    spec_version: 2,
    status: "done",
    artifact_id: "old",
  });
  t.acceptances!.push({
    artifact_id: "old",
    acceptance_id: "acc_fixture",
    spec_version: 2,
    verdict: "changes_required",
    issues: ["explicit current target"],
  });
  expect(currentWork(t).artifacts.map((a) => a.artifact_id)).toEqual(["old"]);
  expect(currentWork(t).issues.map((v) => v.issues)).toEqual([["explicit current target"]]);
  t.lifecycle = "completed";
  t.current_artifact_id = "current";
  t.acceptances!.push({
    artifact_id: "current",
    acceptance_id: "acc_fixture",
    spec_version: 2,
    verdict: "passed",
    issues: [],
  });
  expect(currentWork(t).artifacts.map((a) => a.artifact_id)).toEqual(["current"]);
});

test("acceptance generations never rank distinct artifacts of the same spec", () => {
  const t = detail();
  t.runs![0]!.spec_version = 2;
  t.actions!.push(
    {
      action_id: "accept-old",
      kind: "accept",
      status: "done",
      generation: 7,
      spec_version: 2,
      artifact_id: "old",
    },
    {
      action_id: "accept-new",
      kind: "accept",
      status: "done",
      generation: 1,
      spec_version: 2,
      artifact_id: "current",
    },
  );
  expect(currentWork(t).artifacts.map((a) => a.artifact_id)).toEqual(["current"]);
  t.current_artifact_id = "old";
  expect(currentWork(t).artifacts.map((a) => a.artifact_id)).toEqual(["old"]);
  t.current_artifact_id = "missing";
  expect(currentWork(t).artifacts).toEqual([]);
});
