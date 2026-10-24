// One owner write through POST /api/v1/ops, under a fresh request ID: the
// buttons' actions, such as stopping a reply, answering a decision with an
// effect or pausing a task. A write without an answer is looked up once,
// never sent again blindly (invariant 4).
import { ApiError, NetworkError } from "../api/client.ts";
import type {
  APIResponse,
  Code,
  Envelope,
  OpStatus,
  RequestView,
  TaskSummary,
} from "../api/generated/api.ts";
import { newRequestId } from "../api/ids.ts";

/** The part of the API client a write uses. */
export interface OpSender {
  submit(envelope: Envelope): Promise<APIResponse>;
  lookup(requestId: string): Promise<RequestView | null>;
}

export type OpOutcome =
  | { kind: "done"; result: unknown }
  /** Someone changed the object first; current is the task as it is now. */
  | { kind: "stale"; message: string; current?: TaskSummary }
  | { kind: "refused"; message: string; code?: Code }
  /** tend may or may not have carried it out. */
  | { kind: "unknown"; message: string };

export async function runOp(
  sender: OpSender,
  op: Omit<Envelope, "request_id"> & { request_id?: string },
  now: () => number = Date.now,
): Promise<OpOutcome> {
  const envelope: Envelope = { ...op, request_id: op.request_id ?? newRequestId(now()) };
  let res: APIResponse;
  try {
    res = await sender.submit(envelope);
  } catch (err) {
    if (err instanceof ApiError) {
      return { kind: "refused", message: err.message, ...(err.code ? { code: err.code } : {}) };
    }
    if (!(err instanceof NetworkError)) {
      throw err;
    }
    try {
      const view = await sender.lookup(envelope.request_id);
      if (view) {
        return settleOp(view.status, view.result, view.error);
      }
      return { kind: "unknown", message: "tend 没有收到这个操作，可以再试一次" };
    } catch {
      return { kind: "unknown", message: "tend 没有回应，不知道操作是否生效" };
    }
  }
  if (!res.operation) {
    return { kind: "unknown", message: "tend 的回答里没有结果" };
  }
  const outcome = settleOp(res.operation.status, res.operation.result, res.operation.error);
  if (outcome.kind === "stale" && res.current) {
    outcome.current = res.current;
  }
  return outcome;
}

export function settleOp(
  status: OpStatus,
  result: unknown,
  error: { code: Code; message: string } | undefined,
): OpOutcome {
  switch (status) {
    case "succeeded":
    case "accepted":
      return { kind: "done", result };
    case "failed":
      if (error?.code === "stale_revision") {
        return { kind: "stale", message: error.message };
      }
      return {
        kind: "refused",
        message: error?.message ?? "tend 拒绝了这个操作",
        ...(error ? { code: error.code } : {}),
      };
    case "unknown":
      return { kind: "unknown", message: error?.message ?? "tend 无法确认结果" };
  }
}
