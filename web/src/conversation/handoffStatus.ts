import type { HandoffState } from "./handoff.ts";

// Pending also includes main-conversation work; it does not locate a provider queue.
export function handoffStatus(state: HandoffState): string {
  const view = state.view;
  if (view?.enabled === false) return "未启用托管主会话";
  if (!view && !state.readError) return "正在读取…";
  if (state.saving) return "请求提交中";
  if (state.pendingId) return "操作结果待核对";
  if (state.readError) return "摘要状态暂不可读取";
  if (state.error) return "操作未完成";
  if (view?.preparing) return "正在生成预备摘要";
  if (state.preparationRequestId) return "摘要请求已提交，状态待更新";
  if (view?.maintenance?.status === "unknown") return "上次请求结果待确认";
  if (view?.maintenance?.status === "failed") return "上次请求未完成";
  if (["pending", "sending"].includes(view?.maintenance?.status ?? ""))
    return "摘要或交接请求尚未完成";
  if (view?.pending) return "主会话仍有未结工作";
  if (view?.summary?.current) return "预备摘要已就绪";
  return view?.summary ? "预备摘要已过期" : "尚无预备摘要";
}
