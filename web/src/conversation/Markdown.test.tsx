import { renderToStaticMarkup } from "react-dom/server";
import { expect, test, vi } from "vitest";
import { Markdown } from "./Markdown.tsx";

vi.mock("../tasks/FilePreview.tsx", () => ({
  FilePreview: ({ file }: { file: { name: string } }) => <button>{file.name}</button>,
}));

test("resolved linked images do not wrap preview controls in navigating anchors", () => {
  const output = renderToStaticMarkup(
    <Markdown
      text="[![shot](screen.png)](https://example.com/full-size)"
      files={[{ file_id: "file_example", name: "screen.png", size: 1, sha256: "hash" }]}
    />,
  );
  expect(output).toContain("<button>screen.png</button>");
  expect(output).not.toContain("<a ");
});

test("ordinary links and unresolved image descriptions remain readable", () => {
  const output = renderToStaticMarkup(
    <Markdown text="[Report](https://example.com/report) ![unavailable](https://example.com/screen.png)" />,
  );
  expect(output).toContain('href="https://example.com/report"');
  expect(output).toContain("unavailable");
  expect(output).not.toContain("<img");
});
