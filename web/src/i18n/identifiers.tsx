import { notify } from "../app/notice.ts";
import { useState } from "react";
import { Button } from "../ui/index.ts";

export function shortHash(hash: string): string {
  return hash.slice(0, 7);
}

export function CopyIdentifier({ value }: { value: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <>
      <code title={value}>{value}</code>{" "}
      <Button
        size="s"
        variant="secondary"
        onClick={() =>
          void navigator.clipboard
            .writeText(value)
            .then(() => setCopied(true))
            .catch(() => notify("复制失败，请选择标识后手动复制。", "warn"))
        }
      >
        {copied ? "已复制" : "复制"}
      </Button>
    </>
  );
}

export function CommitHash({ value }: { value: string }) {
  return (
    <>
      <code title={value}>{shortHash(value)}</code>{" "}
      <Button
        size="s"
        variant="secondary"
        aria-label="复制完整提交哈希"
        onClick={() =>
          void navigator.clipboard
            .writeText(value)
            .catch(() => notify("复制失败，请选择哈希后手动复制。", "warn"))
        }
      >
        复制
      </Button>
    </>
  );
}
