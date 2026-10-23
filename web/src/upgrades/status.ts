export const batchStatuses: Record<string, string> = {
  running: "发布进行中",
  attention: "发布需要处理",
  completed: "发布已完成",
  cancelled: "发布已取消",
};
export const batchPhases: Record<string, string> = {
  preflight: "发布预检查",
  server_draining: "等待主会话结束",
  server_ready: "服务准备切换",
  server_checking: "服务健康确认",
  runners: "逐台更新执行主机",
  completed: "完成",
  cancelled: "已取消",
};
export const hostPhases: Record<string, string> = {
  queued: "等待更新",
  draining: "排空中",
  ready: "已排空",
  installing: "安装中",
  checking: "健康确认",
  restored: "已恢复接单",
  rolled_back: "已回退旧版本",
  skipped: "已跳过",
};
export const noticeTitles: Record<string, string> = {
  task_completed: "任务完成",
  upgrade_started: "发布开始",
  upgrade_host_restored: "主机已恢复",
  upgrade_completed: "发布完成",
  upgrade_attention: "发布需要处理",
  upgrade_cancelled: "发布已取消",
};
export function noticeTitle(type?: string): string {
  return type ? (noticeTitles[type] ?? "系统通知") : "任务完成";
}
