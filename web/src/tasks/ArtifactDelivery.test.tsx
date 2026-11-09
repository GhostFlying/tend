import { renderToStaticMarkup } from "react-dom/server";
import { expect, test, vi } from "vitest";
import { ArtifactDelivery } from "./ArtifactDelivery.tsx";

vi.mock("./FilePreview.tsx", () => ({
  FilePreview: ({ file }: { file: { name: string } }) => <button>阅读 {file.name}</button>,
}));

test("the explicit report leads, with supporting files and checks available on demand", () => {
  const output = renderToStaticMarkup(
    <ArtifactDelivery
      value={{
        report: {
          body: "调查结论正文，不用文件名猜测替换。",
          evidence: ["原始证据"],
          uncertainties: ["截图瞬间状态仍未知。"],
        },
        files: [{ file_id: "file_source", name: "report.md", size: 17_000, sha256: "hash" }],
      }}
    />,
  );
  expect(output.indexOf("调查结论正文")).toBeLessThan(output.indexOf("report.md"));
  expect(output).toContain("<summary>原件与证据");
  expect(output).toContain("截图瞬间状态仍未知。");
  expect(output).not.toContain("<details open");
});

test("compact reports use a bounded Unicode summary rather than hiding full content with CSS", () => {
  const value = {
    report: { body: "段😀".repeat(700) + "全文末尾", uncertainties: ["具体未知项"] },
  };
  const preview = renderToStaticMarkup(<ArtifactDelivery compact value={value} />);
  expect(preview).toContain("报告摘要");
  expect(preview).toContain("段😀".repeat(600));
  expect(preview).toContain("…");
  expect(preview).not.toContain("全文末尾");
  expect(preview).not.toContain("具体未知项");
  const full = renderToStaticMarkup(<ArtifactDelivery value={value} />);
  expect(full).toContain("报告正文");
  expect(full).toContain("全文末尾");
  expect(full).toContain("具体未知项");
});
