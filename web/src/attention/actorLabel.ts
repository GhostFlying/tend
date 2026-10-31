import type { Principal } from "../api/generated/api.ts";

export function actorLabel(actor: Principal): string {
  if (actor.kind === "owner") return "你";
  if (actor.kind === "main_agent") return "主 Agent";
  if (actor.kind === "run" && actor.role !== "issue_manager") return "执行 Agent";
  return "负责人 Agent";
}
