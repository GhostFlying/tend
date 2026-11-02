import type { AgentInventoryRuntime } from "../api/generated/api.ts";

/** An environment combines its execution adapter and declared CLI. */
export function runtimeLabel(
  runtime: Pick<AgentInventoryRuntime, "kind" | "cli"> | undefined,
): string {
  if (!runtime) return "未登记";
  const kind =
    runtime.kind === "orca-worker"
      ? "orca"
      : runtime.kind === "appserver-worker"
        ? "app-server"
        : runtime.kind === "central-traex-worker"
          ? "central"
          : runtime.kind === "hosted-manager"
            ? "hosted"
            : runtime.kind;
  return `${kind}/${runtime.cli || "CLI 未记录"}`;
}
