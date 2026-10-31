import type { Principal } from "../api/generated/api.ts";

export function actorLabel(actor: Principal): string {
  if (actor.kind === "owner") return "你";
  if (actor.kind === "main_agent") return "主 Agent";
  if (actor.kind === "run" && actor.role !== "issue_manager") return "执行 Agent";
  if (actor.kind === "background_agent" || actor.kind === "run") return "负责人 Agent";
  if (actor.kind === "runner") return "执行主机";
  return "tend";
}
