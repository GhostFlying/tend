import type { AgentInventoryRuntime } from "../api/generated/api.ts";

type Runtime = Pick<AgentInventoryRuntime, "kind" | "cli">;

export function cliLabel(cli: string | undefined): string {
  return cli === "codex" ? "Codex" : cli === "traex" ? "TraeX" : cli || "";
}

/** Presentation uses declared metadata; IDs and executable names are not CLI identities. */
export function runtimeMethod(runtime: Runtime | undefined): string {
  if (!runtime) return "未登记";
  const adapter =
    runtime.kind === "orca-worker"
      ? "Orca"
      : runtime.kind === "appserver-worker" || runtime.kind === "hosted-manager"
        ? "app-server"
        : runtime.kind === "central-traex-worker"
          ? "集中执行"
          : runtime.kind;
  return [cliLabel(runtime.cli), adapter].filter(Boolean).join(" · ");
}

/** Entry method is adapter/CLI metadata; the environment is a separate object. */
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
