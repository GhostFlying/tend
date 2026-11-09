import { renderToStaticMarkup } from "react-dom/server";
import { expect, test, vi } from "vitest";
import { ArtifactDelivery } from "./ArtifactDelivery.tsx";

vi.mock("./FilePreview.tsx", () => ({
  FilePreview: ({ file }: { file: { name: string } }) => <button>阅读 {file.name}</button>,
}));

test("the explicit report leads, with supporting files and checks available on demand", () => {
  const output = renderToStaticMarkup(
    <ArtifactDelivery
      compact
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
