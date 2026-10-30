import { createRef } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { expect, test } from "vitest";
import {
  Button,
  Field,
  Input,
  Checkbox,
  Select,
  Tabs,
  SegmentedControl,
  StatusChip,
  Dialog,
  Sheet,
  Menu,
} from "./index.ts";
import { choiceIndex } from "./Choices.tsx";

test("button defaults to a non-submit button and forwards native props", () => {
  const html = renderToStaticMarkup(
    <Button ref={createRef<HTMLButtonElement>()} disabled name="save" aria-label="保存">
      保存
    </Button>,
  );
  expect(html).toContain('type="button"');
  expect(html).toContain('disabled=""');
  expect(html).toContain('name="save"');
  expect(html).toContain('aria-label="保存"');
  expect(renderToStaticMarkup(<Button type="submit" />)).toContain('type="submit"');
});

test("field associates label, existing descriptions, hint and error with its control", () => {
  const html = renderToStaticMarkup(
    <Field id="goal" label="要求" hint="简短说明" error="请填写">
      <Input aria-describedby="external" />
    </Field>,
  );
  expect(html).toContain('for="goal"');
  expect(html).toContain('id="goal"');
  expect(html).toContain('aria-describedby="external goal-hint goal-error"');
  expect(html).toContain('aria-invalid="true"');
  expect(html).toContain('id="goal-error"');
  expect(html).toContain('role="alert"');
  const valid = renderToStaticMarkup(
    <Field label="项目">
      <Select id="project">
        <option>全部</option>
      </Select>
    </Field>,
  );
  expect(valid).toContain('for="project"');
  expect(valid).not.toContain("aria-invalid");
  expect(valid).not.toContain("aria-describedby");
});

test("checkbox keeps native checked and disabled semantics", () => {
  expect(renderToStaticMarkup(<Checkbox label="启用" checked disabled readOnly />)).toMatch(
    /type="checkbox"/,
  );
});

test("choice keyboard navigation wraps, skips disabled entries and supports boundaries", () => {
  const disabled = [false, true, false];
  expect(choiceIndex("ArrowRight", 0, disabled)).toBe(2);
  expect(choiceIndex("ArrowDown", 2, disabled)).toBe(0);
  expect(choiceIndex("ArrowLeft", 0, disabled)).toBe(2);
  expect(choiceIndex("Home", 2, disabled)).toBe(0);
  expect(choiceIndex("End", 0, disabled)).toBe(2);
  expect(choiceIndex("Enter", 0, disabled)).toBeUndefined();
  expect(choiceIndex("ArrowRight", 0, [true])).toBeUndefined();
});

const items = [
  { value: "one", label: "当前", panel: "当前内容" },
  { value: "two", label: "历史", panel: "历史内容" },
];
test("tabs and segmented controls expose a single keyboard entry and selected semantics", () => {
  const tabs = renderToStaticMarkup(
    <Tabs label="任务" items={items} value="one" onChange={() => {}} />,
  );
  expect(tabs).toContain('role="tablist"');
  expect(tabs.match(/<button[^>]*tabindex="0"/g)).toHaveLength(1);
  expect(tabs.match(/aria-selected="true"/g)).toHaveLength(1);
  expect(tabs).toContain('role="tabpanel"');
  expect(tabs).toContain('hidden=""');
  const segmented = renderToStaticMarkup(
    <SegmentedControl label="范围" items={items} value="two" onChange={() => {}} />,
  );
  expect(segmented).toContain('role="radiogroup"');
  expect(segmented.match(/aria-checked="true"/g)).toHaveLength(1);
  expect(segmented.match(/tabindex="0"/g)).toHaveLength(1);
});

test("status and overlays keep readable labels and native semantics", () => {
  expect(renderToStaticMarkup(<StatusChip tone="ok">完成</StatusChip>)).toContain(
    'aria-hidden="true"',
  );
  for (const Component of [Dialog, Sheet]) {
    const html = renderToStaticMarkup(
      <Component open={false} title="详情" onClose={() => {}}>
        内容
      </Component>,
    );
    expect(html).toContain("<dialog");
    expect(html).toContain("aria-labelledby=");
    expect(html).toContain('aria-label="关闭详情"');
    expect(html).not.toContain('open=""');
  }
  const menu = renderToStaticMarkup(
    <Menu label="更多" items={[{ id: "one", label: "设置", onSelect: () => {} }]} />,
  );
  expect(menu).toContain('aria-haspopup="menu"');
  expect(menu).toContain('popover="auto"');
  expect(menu).toContain('role="menuitem"');
});
