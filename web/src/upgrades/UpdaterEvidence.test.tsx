import { renderToStaticMarkup } from "react-dom/server";
import { expect, test } from "vitest";
import type { UpdaterSelfObservation } from "../api/generated/api.ts";
import { UpdaterEvidence } from "./UpdaterEvidence.tsx";

function observation(): UpdaterSelfObservation {
  const updater = {
    pid: 41,
    start_ticks: 10,
    uid: 1000,
    sha256: "a".repeat(64),
    launch_id: "updater",
  };
  const service = {
    ...updater,
    pid: 42,
    start_ticks: 11,
    sha256: "b".repeat(64),
    launch_id: "service",
  };
  return {
    instance_id: "server",
    process: updater,
    supervised_process: service,
    receipt: {
      instance_id: "server",
      request_id: "original-request",
      upgrade_id: "batch",
      release_set_sha256: "c".repeat(64),
      previous: updater,
      installed: updater,
      retained_process: service,
      completed_at_millis: 1000,
    },
  };
}

test("missing handoff evidence remains pending", () => {
  const html = renderToStaticMarkup(<UpdaterEvidence />);
  expect(html).toContain("等待本批交接证明");
  expect(html).not.toContain("已取得");
});

test("verified handoff exposes separate updater, service and original receipt identities", () => {
  const html = renderToStaticMarkup(<UpdaterEvidence observation={observation()} />);
  expect(html).toContain("已取得本批交接证明");
  expect(html).toContain("最后观察的更新器");
  expect(html).toContain("最后观察的服务");
  expect(html).toContain("交接时保留的服务");
  expect(html).toContain("original-request");
  expect(html).toContain("a".repeat(64));
  expect(html).toContain("b".repeat(64));
});

test("preparation rejection takes precedence over a historical success receipt", () => {
  const o = observation();
  o.rejection = {
    instance_id: "server",
    request_id: "rejected-request",
    upgrade_id: "batch",
    release_set_sha256: "d".repeat(64),
    rejected_at_millis: 2000,
  };
  const html = renderToStaticMarkup(<UpdaterEvidence observation={o} />);
  expect(html).toContain("准备已拒绝，未执行替换");
  expect(html).toContain("rejected-request");
  expect(html).not.toContain("已取得本批交接证明");
  expect(html).not.toContain("original-request");
});
