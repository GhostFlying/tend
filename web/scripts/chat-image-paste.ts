/// <reference lib="dom" />
// Real X clipboard regression against an isolated tend-sandbox, never production.
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { mkdir, writeFile } from "node:fs/promises";
import { createHash } from "node:crypto";
import { join } from "node:path";
import { chromium, type Page, type Request } from "playwright";
const origin = process.env.TEND_PASTE_ORIGIN;
const output = process.env.TEND_PASTE_OUTPUT;
assert(origin && output, "Set TEND_PASTE_ORIGIN to an isolated sandbox and TEND_PASTE_OUTPUT");
assert(process.env.DISPLAY, "A real X server clipboard is required");
await mkdir(output, { recursive: true });
const browser = await chromium.launch({ headless: false, args: ["--no-sandbox"] });
const hash = (bytes: Buffer) => createHash("sha256").update(bytes).digest("hex");
const results: unknown[] = [];
const clips: ReturnType<typeof spawn>[] = [];
try {
  const source = await browser.newPage({ viewport: { width: 280, height: 160 } });
  await source.setContent(
    '<body style="margin:0;background:#284ed8;color:white;font:28px sans-serif"><p>Clipboard 原件</p><p>PNG ✓</p></body>',
  );
  const png = await source.screenshot({ path: join(output, "clipboard-source.png") });
  await source.close();
  const page = await browser.newPage({ viewport: { width: 1400, height: 950 } });
  await page.addInitScript(() => {
    const revoked: string[] = [];
    const revoke = URL.revokeObjectURL.bind(URL);
    URL.revokeObjectURL = (url) => {
      revoked.push(url);
      revoke(url);
    };
    Object.assign(window, { revokedPreviewURLs: revoked });
  });
  const api = async (path: string, allowMissing = false) =>
    page.evaluate(
      async ({ path, allowMissing }) => {
        const response = await fetch(path, {
          headers: { Authorization: `Bearer ${localStorage.getItem("tend.token")}` },
        });
        if (allowMissing && response.status === 404) return null;
        if (!response.ok) throw new Error(`API ${response.status}`);
        return response.json();
      },
      { path, allowMissing },
    );
  await page.goto(origin);
  await page.getByRole("textbox", { name: "发消息给 Tend", exact: true }).waitFor();
  const issues = await api("/api/v1/issues");
  const detail = await api(`/api/v1/issues/${issues.items[0].issue_id}`);
  const issueId = detail.issue_id ?? detail.issue?.issue_id;
  assert(issueId);
  const paste = async (page: Page, text = false) => {
    const child = spawn(
      "xclip",
      ["-selection", "clipboard", "-t", text ? "UTF8_STRING" : "image/png", "-i", "-quiet"],
      { stdio: ["pipe", "ignore", "ignore"] },
    );
    clips.push(child);
    child.stdin!.end(text ? Buffer.from("原生中文粘贴") : png);
    await new Promise((resolve) => setTimeout(resolve, 120));
    await page.keyboard.press("Control+v");
  };
  for (const [label, path, name] of [
    ["im", "/", "发消息给 Tend"],
    ["main", "/diagnostics/main", "消息"],
    ["issue", `/issues/${issueId}`, "消息"],
  ] as const) {
    await page.goto(origin + path);
    const input = page.getByRole("textbox", { name, exact: true });
    await input.waitFor();
    await input.fill("");
    await input.focus();
    const writes: { operation: string; payload: Record<string, unknown> }[] = [];
    const uploads: {
      response: Promise<{ operation: { result: { file_id: string } } }>;
    }[] = [];
    const listener = (request: Request) => {
      if (request.method() !== "POST") return;
      if (request.url().includes("/conversation/attachments?"))
        uploads.push({
          response: request.response().then((r) => r!.json()),
        });
      else {
        try {
          const body = request.postDataJSON();
          if (body?.operation) writes.push(body);
        } catch {
          /* Non-JSON upload. */
        }
      }
    };
    page.on("request", listener);
    const pending = page.getByRole("list", { name: "待发送附件", exact: true });
    // Synthetic mixed event verifies DOM integration; the real X paste follows below.
    await input.fill("草稿SELECT尾部");
    await input.evaluate(
      (element, bytes) => {
        const input = element as HTMLTextAreaElement;
        input.setSelectionRange(2, 8);
        const data = new DataTransfer();
        data.items.add(new File([new Uint8Array(bytes)], "mixed.png", { type: "image/png" }));
        data.setData("text/plain", "图片说明");
        input.dispatchEvent(
          new ClipboardEvent("paste", { clipboardData: data, bubbles: true, cancelable: true }),
        );
      },
      [...png],
    );
    await pending.locator("img").waitFor();
    assert.equal(await input.inputValue(), "草稿图片说明尾部");
    assert.equal(await input.evaluate((e) => (e as HTMLTextAreaElement).selectionStart), 6);
    await input.press("Control+z");
    assert.equal(await input.inputValue(), "草稿SELECT尾部", "mixed paste must preserve undo");
    assert.equal(await pending.locator("li").count(), 1, "undo text does not send or drop files");
    await input.press("Control+y");
    assert.equal(await input.inputValue(), "草稿图片说明尾部", "mixed paste supports redo");
    assert.equal(
      await pending.locator("li").count(),
      1,
      "items and files views must not double add",
    );
    await input.dispatchEvent("keydown", {
      key: "Enter",
      code: "Enter",
      isComposing: true,
      bubbles: true,
    });
    assert.equal(writes.length, 0, "IME confirmation must not send");
    await pending.getByRole("button", { name: /移除/ }).click();
    await input
      .locator("..")
      .locator('input[type="file"]')
      .setInputFiles({ name: "manual.png", mimeType: "image/png", buffer: png });
    await pending.locator("img").waitFor();
    await pending.getByRole("button", { name: /移除/ }).click();
    await input.fill("");
    await input.focus();
    await paste(page);
    await pending.locator("img").waitFor();
    assert.equal(await pending.locator("li").count(), 1);
    assert.equal(writes.length, 0, "paste must not send");
    assert.equal(uploads.length, 0, "paste must not upload");
    await page.screenshot({ path: join(output, `${label}-preview.png`) });
    await input.locator("..").screenshot({ path: join(output, `${label}-composer.png`) });
    const previewURL = await pending.locator("img").getAttribute("src");
    await pending.getByRole("button", { name: /移除/ }).click();
    await pending.waitFor({ state: "detached" });
    assert(
      await page.evaluate(
        (url) =>
          (window as unknown as { revokedPreviewURLs: string[] }).revokedPreviewURLs.includes(url!),
        previewURL,
      ),
    );
    await page.screenshot({ path: join(output, `${label}-removed.png`) });
    await input.fill("草稿SELECT尾部");
    await input.evaluate((e) => {
      (e as HTMLTextAreaElement).setSelectionRange(2, 8);
    });
    await input.focus();
    await paste(page, true);
    await page.waitForTimeout(100);
    assert.equal(await input.inputValue(), "草稿原生中文粘贴尾部");
    const marker = `clipboard-${label}-${Date.now()}`;
    await input.fill(marker);
    await paste(page);
    await pending.locator("img").waitFor();
    await paste(page);
    assert.equal(await pending.locator("li").count(), 2, "repeated same-name paste retains both");
    await pending.getByRole("button", { name: /移除/ }).last().click();
    const sentPreviewURL = await pending.locator("img").getAttribute("src");
    await page.getByRole("button", { name: "发送", exact: true }).click();
    await pending.waitFor({ state: "detached" });
    assert(
      await page.evaluate(
        (url) =>
          (window as unknown as { revokedPreviewURLs: string[] }).revokedPreviewURLs.includes(url!),
        sentPreviewURL,
      ),
    );
    for (let i = 0; i < 100 && (!uploads.length || !writes.length); i++)
      await page.waitForTimeout(100);
    assert.equal(uploads.length, 1);
    const upload = await uploads[0]!.response;
    const envelope = writes.find(
      (w) => w.operation === (label === "im" ? "messaging.send" : "conversation.submit_message"),
    );
    assert(envelope, JSON.stringify(writes));
    const fileId = upload.operation.result.file_id;
    assert(fileId, JSON.stringify(upload));
    const messageId = (envelope.payload as { message_id?: string }).message_id;
    assert(messageId, "the first composition retains its real message identity");
    let received;
    for (let attempt = 0; attempt < 100; attempt++) {
      received = await api(
        label === "im"
          ? `/api/v1/messaging/messages/${messageId}`
          : `/api/v1/messages/${messageId}`,
        true,
      );
      if (received?.message_id === messageId) break;
      await page.waitForTimeout(100);
    }
    assert.equal(received?.message_id, messageId);
    assert.equal(received.body, marker);
    assert.equal(received.attachments[0].file_id, fileId);
    if (label !== "im") {
      const target = (envelope.payload as { conversation_id?: string }).conversation_id;
      const page = await api(
        `/api/v1/conversation${target ? `?conversation_id=${encodeURIComponent(target)}` : ""}`,
      );
      const received = page.items.find((m: { body: string }) => m.body === marker);
      assert(received, JSON.stringify(page));
      assert.equal(received.attachments[0].file_id, fileId);
    }
    const info = await api(`/api/v1/conversation/attachments/${fileId}/download-url`);
    const downloaded = await (await page.request.get(new URL(info.url, origin).href)).body();
    // Chromium DevTools omits Blob upload postData; compare source and stored originals.
    assert.equal(hash(downloaded), hash(png));
    await page.waitForTimeout(500);
    await page.screenshot({ path: join(output, `${label}-sent.png`) });
    results.push({
      label,
      path,
      marker,
      upload,
      envelope,
      message: received,
      sha256: hash(downloaded),
      bytes: downloaded.length,
    });
    page.off("request", listener);
  }
  // Only this failure response is mocked; recovery uses the actual sandbox again.
  await page.goto(origin);
  const input = page.getByRole("textbox", { name: "发消息给 Tend", exact: true });
  await input.fill("clipboard-upload-failure-recovery");
  await input.focus();
  await paste(page);
  await page.route("**/conversation/attachments?*", (route) =>
    route.fulfill({
      status: 422,
      contentType: "application/json",
      body: JSON.stringify({
        error: { code: "invalid_request", message: "验证注入：附件上传失败" },
      }),
    }),
  );
  await page.getByRole("button", { name: "发送", exact: true }).click();
  await page.getByText("验证注入：附件上传失败", { exact: true }).waitFor();
  await page.screenshot({ path: join(output, "im-upload-failed.png") });
  await page.unroute("**/conversation/attachments?*");
  await page.getByRole("button", { name: "恢复为新消息", exact: true }).click();
  const restored = page.getByRole("list", { name: "待发送附件", exact: true });
  await restored.locator("img").waitFor();
  assert.equal(await input.inputValue(), "clipboard-upload-failure-recovery");
  await page.screenshot({ path: join(output, "im-upload-restored.png") });
  const recoveredResponse = page.waitForResponse(
    (response) =>
      response.url().endsWith("/api/v1/ops") &&
      response.request().postDataJSON()?.operation === "messaging.send",
  );
  await page.getByRole("button", { name: "发送", exact: true }).click();
  await restored.waitFor({ state: "detached" });
  const recoveredReceipt = await (await recoveredResponse).json();
  assert.equal(recoveredReceipt.operation.status, "accepted");
  const recoveredMessage = await api(
    `/api/v1/messaging/messages/${recoveredReceipt.operation.result.message_id}`,
  );
  assert.equal(recoveredMessage.body, "clipboard-upload-failure-recovery");
  assert.equal(recoveredMessage.attachments[0].sha256, hash(png));
  // Format coverage uses synthetic file events, with real upload/message/download APIs.
  const formats = await page.evaluate(async (png) => {
    const image = new Image();
    image.src = `data:image/png;base64,${png}`;
    await image.decode();
    const canvas = document.createElement("canvas");
    canvas.width = image.width;
    canvas.height = image.height;
    canvas.getContext("2d")!.drawImage(image, 0, 0);
    return [
      ["image/png", png],
      ["image/jpeg", canvas.toDataURL("image/jpeg").split(",")[1]!],
      ["image/webp", canvas.toDataURL("image/webp").split(",")[1]!],
      // A two-frame GIF: the original must retain both frames.
      [
        "image/gif",
        "R0lGODlhAgACAIEAAP8AAAAAAAAAAAAAACH/C05FVFNDQVBFMi4wAwEAAAAh+QQACgAAACwAAAAAAgACAAAIBgABCAQQEAAh+QQBCgABACwAAAAAAgACAIEAAP8AAAAAAAAAAAAIBgABCAQQEAA7",
      ],
    ];
  }, png.toString("base64"));
  await input.fill("clipboard-original-format-coverage");
  await input.evaluate((element, formats) => {
    const data = new DataTransfer();
    for (const [type, base64] of formats) {
      const bytes = Uint8Array.from(atob(base64!), (c) => c.charCodeAt(0));
      data.items.add(new File([bytes], `original.${type!.split("/")[1]}`, { type: type! }));
    }
    element.dispatchEvent(
      new ClipboardEvent("paste", { clipboardData: data, bubbles: true, cancelable: true }),
    );
  }, formats);
  await restored.locator("img").first().waitFor();
  assert.equal(await restored.locator("li").count(), 4);
  await page.screenshot({ path: join(output, "im-four-formats.png") });
  const formatResponse = page.waitForResponse(
    (response) =>
      response.url().endsWith("/api/v1/ops") &&
      response.request().postDataJSON()?.operation === "messaging.send",
  );
  await page.getByRole("button", { name: "发送", exact: true }).click();
  const formatReceipt = await (await formatResponse).json();
  assert.equal(formatReceipt.operation.status, "accepted");
  const formatMessage = await api(
    `/api/v1/messaging/messages/${formatReceipt.operation.result.message_id}`,
  );
  assert.equal(formatMessage.attachments.length, 4);
  const formatResults = [];
  for (const [type, base64] of formats) {
    const file = formatMessage.attachments.find(
      (file: { media_type: string }) => file.media_type === type,
    );
    assert(file);
    const info = await api(`/api/v1/conversation/attachments/${file.file_id}/download-url`);
    const bytes = await (await page.request.get(new URL(info.url, origin).href)).body();
    assert.equal(hash(bytes), hash(Buffer.from(base64!, "base64")));
    formatResults.push({ type, sha256: hash(bytes), bytes: bytes.length });
  }
  results.push({
    formats: formatResults,
    source: "synthetic DataTransfer; actual server original bytes, including two-frame GIF",
  });
  results.push({
    uploadFailure:
      "mock HTTP 422; visible error; restored original Blob preview and text; explicit send retried against actual sandbox",
    supplemental:
      "synthetic mixed text/selection, IME Enter guard, manual attachment, object URL revocation on removal and send tested for all three surfaces",
  });
  await writeFile(
    join(output, "desktop-results.json"),
    JSON.stringify(
      {
        browser: browser.version(),
        method:
          "Xvfb + xclip image/png + trusted Control+v; actual sandbox API/storage; scripted agent only",
        results,
      },
      null,
      2,
    ),
  );
  console.log(
    "Real clipboard passed for IM, main and Issue; downloaded originals match uploaded/source PNG bytes.",
  );
} finally {
  for (const child of clips) child.kill();
  await browser.close();
}
