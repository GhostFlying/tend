// Associate current evidence using explicit spec and artifact identities.
import type { AcceptanceView, TaskDetail } from "../api/generated/api.ts";

export function currentWork(task: TaskDetail) {
  const actions = task.actions ?? [];
  const runs = task.runs ?? [];
  const all = task.artifacts ?? [];
  const acceptances = (task.acceptances ?? []).filter((a) => a.spec_version === task.spec_version);
  const latest = new Map<string, AcceptanceView>();
  for (const verdict of acceptances) latest.set(verdict.artifact_id, verdict);
  // A completed task's passed verdict names the accepted artifact directly.
  const passed =
    task.lifecycle === "completed"
      ? acceptances.filter((a) => a.verdict === "passed").at(-1)?.artifact_id
      : undefined;
  const target = actions
    .filter(
      (a) =>
        a.kind === "accept" &&
        a.spec_version === task.spec_version &&
        a.artifact_id &&
        a.status !== "superseded" &&
        a.status !== "cancelled",
    )
    .sort((a, b) => b.generation - a.generation)[0]?.artifact_id;
  const selected = passed ?? target;
  let artifacts = selected ? all.filter((a) => a.artifact_id === selected) : [];
  if (!selected) {
    // Generations are comparable only within this spec. An older spec's
    // generation 1 must never masquerade as the new spec's generation 1.
    const produced = all.filter((artifact) =>
      runs.some(
        (r) =>
          r.run_id === artifact.run_id &&
          r.role === "implement" &&
          r.spec_version === task.spec_version,
      ),
    );
    const generation = Math.max(
      0,
      ...produced.map(
        (artifact) => runs.find((r) => r.run_id === artifact.run_id)?.generation ?? 0,
      ),
    );
    artifacts = produced.filter(
      (artifact) => runs.find((r) => r.run_id === artifact.run_id)?.generation === generation,
    );
    if (!artifacts.length) {
      const explicit = acceptances.at(-1)?.artifact_id;
      artifacts = all.filter((a) => a.artifact_id === explicit);
    }
  }
  const ids = new Set(artifacts.map((a) => a.artifact_id));
  const verdicts = [...latest.values()].filter((v) => ids.has(v.artifact_id));
  const needsIssues =
    task.lifecycle === "changes_required" ||
    task.block_reason === "waiting_decision" ||
    task.block_reason === "acceptance_env_unavailable";
  return {
    artifacts,
    verdicts,
    issues: needsIssues ? verdicts.filter((v) => v.verdict !== "passed") : [],
  };
}
