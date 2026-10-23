import { describe, expect, test } from "vitest";
import type { HostView } from "../api/generated/api.ts";
import { hostMaintaining, hostMaintenanceLabel, hostOnline } from "./status.ts";

const host: HostView = {
  host_id: "runner",
  os: "linux",
  arch: "amd64",
  worker: "appserver",
  labels: [],
  slots: 2,
  open_runs: 1,
};

describe("independent host observations", () => {
  test("legacy missing heartbeat is unreported and never implicitly offline", () => {
    expect(hostOnline(host)).toBe("心跳未报告");
    expect(hostMaintaining(host)).toBe(false);
  });
  test("an online ready host remains in maintenance despite free slots", () => {
    const ready = { ...host, open_runs: 0, online: true, maintenance: { mode: "ready" } };
    expect(hostOnline(ready)).toBe("在线");
    expect(hostMaintaining(ready)).toBe(true);
    expect(hostMaintenanceLabel(ready)).toBe("维护 · 已排空");
  });
  test("offline and occupied hosts retain independent maintenance facts", () => {
    const draining = {
      ...host,
      online: false,
      maintenance: { mode: "maintenance", remaining_runs: 1 },
    };
    expect(hostOnline(draining)).toBe("离线");
    expect(hostMaintaining(draining)).toBe(true);
    expect(hostMaintenanceLabel(draining)).toBe("维护中");
    expect(hostMaintaining({ ...host, online: true, maintenance: { mode: "normal" } })).toBe(false);
  });
});
