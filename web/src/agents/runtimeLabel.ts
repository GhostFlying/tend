import type { AgentInventoryRuntime } from "../api/generated/api.ts";

/** Presentation uses the registered adapter kind and its declared CLI, never presence. */
export function runtimeLabel(
  runtime: Pick<AgentInventoryRuntime, "kind" | "cli"> | undefined,
): string {
  if (!runtime) return "未登记";
  const cli = runtime.cli === "traex" ? "TraeX" : runtime.cli;
  if (runtime.kind === "orca-worker") return cli ? `Orca · ${cli}` : "Orca";
  if (runtime.kind === "appserver-worker") return "TraeX";
  if (runtime.kind === "hosted-manager") return cli || "实际类型未记录";
  return cli || runtime.kind;
}
