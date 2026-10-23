import type { HostView } from "../api/generated/api.ts";

export function hostOnline(host: HostView): string {
  return host.online === undefined ? "心跳未报告" : host.online ? "在线" : "离线";
}

export function hostMaintaining(host: HostView): boolean {
  return !!host.maintenance?.mode && host.maintenance.mode !== "normal";
}

export function hostMaintenanceLabel(host: HostView): string {
  return host.maintenance?.mode === "ready" ? "维护 · 已排空" : "维护中";
}
