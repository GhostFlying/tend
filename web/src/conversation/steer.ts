import type { MainSteerView } from "../api/generated/api.ts";

export function steerNotice(steer: MainSteerView | undefined): string | undefined {
  if (!steer) return undefined;
  switch (steer.status) {
    case "accepted":
      return "主代理将在原生会话的安全边界读取；执行中的工具可能需要先完成。";
    case "sending":
      return "正在送入当前回合，尚未收到原生确认。";
    case "waiting":
      if (steer.reason?.startsWith("hosted source schema"))
        return "当前主会话需要更新后才能实时补充；消息继续排队。";
      if (steer.reason?.startsWith("native executor"))
        return "当前执行器不支持实时补充，消息将等待当前回合结束。";
      if (steer.reason?.startsWith("attachment or message preparation failed"))
        return "消息或附件准备失败，尚未送入原生会话；原文保留在队列中。";
      if (steer.reason?.startsWith("native rejected"))
        return "原生会话拒绝了本轮补充，回合可能已结束或接口不兼容；原文保留在队列中。";
      return "消息尚未送入当前回合；原文保留在队列中，等待后续处理。";
    case "unknown":
      return "无法确认原生会话是否已读取，消息已保留，系统不会自动重发。";
    case "failed":
      return "当前回合处理失败，补充消息及已生成内容均已保留。";
    case "interrupted":
      return "补充消息已进入原生会话，当前回合随后停止。";
    default:
      return undefined;
  }
}
