import type { AgentRecoveryView } from "../api/generated/api.ts";

export function recoveryNotice(recovery?: AgentRecoveryView): string | undefined {
  if (!recovery || recovery.status === "completed") return undefined;
  const attempt = `第 ${recovery.attempts} 次自动续接`;
  switch (recovery.status) {
    case "waiting": {
      const next = recovery.next_attempt_at
        ? new Date(recovery.next_attempt_at).toLocaleTimeString("zh-CN", { hour12: false })
        : "稍后";
      return `模型限流，${next} 尝试${attempt}；等待期间可停止`;
    }
    case "starting":
      return `正在启动${attempt}`;
    case "running":
      return `正在${attempt}`;
    case "unknown":
      return "自动续接结果待核对，需要处理";
    case "exhausted":
      return "自动续接预算已用尽，需要处理";
    case "cancelled":
      return "自动续接已取消";
    default:
      return "自动续接未完成，需要处理";
  }
}
