import { useEffect, useMemo, useState } from "react";
import { newRequestId } from "../api/ids.ts";
import type { Envelope } from "../api/generated/api.ts";
import type { OpSender } from "./ops.ts";
import { DurableOp } from "./durableOp.ts";

export function useDurableOp(sender: OpSender, scope: string) {
  const journal = useMemo(() => new DurableOp(scope), [scope]);
  const [pending, setPending] = useState(false);
  const [original, setOriginal] = useState<Readonly<Envelope> | null>(null);
  const [busy, setBusy] = useState(true);
  const [storageError, setStorageError] = useState("");
  useEffect(() => {
    let active = true;
    void journal.pending().then(
      (request) => {
        if (active) {
          setPending(!!request);
          setOriginal(request ?? null);
          setBusy(false);
        }
      },
      () => {
        if (active) {
          setStorageError("无法打开本地操作记录，尚未发送请求。");
          setBusy(false);
        }
      },
    );
    return () => {
      active = false;
    };
  }, [journal]);
  const execute = async (envelope?: Omit<Envelope, "request_id">) => {
    setBusy(true);
    try {
      return envelope
        ? await journal.submit(sender, { ...envelope, request_id: newRequestId(Date.now()) })
        : await journal.reconcile(sender);
    } finally {
      try {
        const request = await journal.pending();
        setPending(!!request);
        setOriginal(request ?? null);
      } finally {
        setBusy(false);
      }
    }
  };
  return { pending, original, busy, storageError, execute };
}
