import { renderToStaticMarkup } from "react-dom/server";
import { afterEach, expect, test, vi } from "vitest";
import { Markdown } from "./Markdown.tsx";

afterEach(() => vi.unstubAllGlobals());

vi.mock("../tasks/FilePreview.tsx", () => ({
  FilePreview: ({ file, scope }: { file: { name: string }; scope?: string }) => (
    <button data-scope={scope === "conversation/attachments" ? scope : undefined}>
      {file.name}
    </button>
  ),
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

test("original attachment links retain the attachment scope when reading another original", () => {
  const output = renderToStaticMarkup(
    <Markdown
      text="[原始附件](source.md)"
      files={[{ file_id: "file_source", name: "source.md", size: 1, sha256: "hash" }]}
      fileScope="conversation/attachments"
    />,
  );
  expect(output).toContain('data-scope="conversation/attachments"');
});

test("ordinary links and unresolved image descriptions remain readable", () => {
  const output = renderToStaticMarkup(
    <Markdown text="[Report](https://example.com/report) ![unavailable](https://example.com/screen.png)" />,
  );
  expect(output).toContain('href="https://example.com/report"');
  expect(output).toContain("unavailable");
  expect(output).not.toContain("<img");
});

test("an explicit original-file link resolves only its exact immutable manifest", () => {
  const file = { file_id: "file_report", name: "report.md", size: 17000, sha256: "hash" };
  const linked = renderToStaticMarkup(<Markdown text="[阅读完整调查](report.md)" files={[file]} />);
  expect(linked).toContain("<button>report.md</button>");
  expect(linked).not.toContain('href="report.md"');
  const ambiguous = renderToStaticMarkup(
    <Markdown text="[阅读完整调查](report.md)" files={[file, { ...file, file_id: "other" }]} />,
  );
  expect(ambiguous).toContain('href="report.md"');
  expect(ambiguous).not.toContain("<button>");
});

test("same-origin page links become canonical anchors while other links remain external", () => {
  vi.stubGlobal("window", { location: new URL("https://tend.example/") });
  const output = renderToStaticMarkup(
    <Markdown text="[Issue](/?issue=parent) [Task](/?task=child&issue=parent) [Other](https://other.example/issues/parent) [Broken](http://[broken)" />,
  );
  expect(output).toContain('href="/issues/parent"');
  expect(output).toContain('href="/tasks/child?from=parent"');
  expect(output).toContain('href="https://other.example/issues/parent" target="_blank"');
  expect(output).toContain("Broken");
});
