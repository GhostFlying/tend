import { renderToStaticMarkup } from "react-dom/server";
import { expect, test, vi } from "vitest";
import { AgentTurn } from "./Messages.tsx";

vi.mock("./MessageTags.tsx", () => ({ MessageTags: () => null }));
vi.mock("../tasks/FilePreview.tsx", () => ({
  FilePreview: ({ file, scope }: { file: { name: string }; scope: string }) => (
    <button data-scope={scope}>阅读 {file.name}</button>
  ),
}));

test("the final assistant reply exposes its original report in the attachment scope", () => {
  const output = renderToStaticMarkup(
    <AgentTurn
      entry={{
        kind: "agent",
        key: "reply-one",
        replyTo: "owner-one",
        items: [
          {
            message_id: "reply-one",
            author: "main_agent",
            kind: "message",
            seq: 1,
            created_at: "2026-01-01T00:00:00Z",
            assistant_state: "completed",
            body: "已完成调查，完整背景和结论见报告原件。",
            purpose: "final",
            attachments: [
              {
                file_id: "file_report",
                name: "report.md",
                size: 17_000,
                sha256: "a".repeat(64),
                media_type: "text/markdown",
              },
            ],
          },
        ],
      }}
    />,
  );
  expect(output).toContain("已完成调查，完整背景和结论见报告原件。");
  expect(output).toContain("阅读 report.md");
  expect(output).toContain("成果</span>");
  expect(output).toContain("不代表验收或交付状态");
  expect(output).toContain('data-scope="conversation/attachments"');
});
