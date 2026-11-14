# 工具输入契约与可恢复错误

## 业务目标与现有缺口

Issue owner 更新进度时，App 已按 Unicode 字符限制 summary 500、next_step 240，
但共享 schema 只公开 string。工作区 base 同时提供 branch 和 commit 已被拒绝，
输入契约未说明互斥。缺失版本、已结束 Run 及已完成 Task 的错误缺少已知对象身份与
现有恢复入口，调用方容易重复猜测输入。

## 实施范围

1. 在 protocol 复用现有 JSONSchemaConstraints，使工具与 owner API schema
   公开进度长度和工作区 base 互斥；App 使用同源字符限制及具体字段错误。
2. 在现有 protocol.Error.details 中补充有限的字段、限制、当前版本、原 Task/Run
   及恢复入口，只附带该调用已经读取且有权访问的事实，不另行查询或扩展可见范围。
3. 缺少 expected_control_revision 时说明目标及原对象读取入口；已完成 Task 和
   已结束 Run 保留原转换规则，指出同目标续行或状态读取入口。
4. 通过实际生成命令更新 schema；检查 TypeScript 消费者仍保持原有可用输入类型。

不增加权限、租约、unknown 或版本绕过；不新增业务账本、恢复动作或迁移。
新 schema 仅公开 App 已有约束，不收紧历史有效请求。

## 验证

- 超界及边界 Unicode 进度、清空、原 CAS/epoch 拒绝和失败不写入。
- protocol schema 与模型工具 schema 同步；branch/commit 保持可省略及单独选择。
- 已结束 Run、已完成 Task 的现有拒绝仍生效，错误指向原对象与适用入口。
- 生成类型普通消费者、定向测试和 race/vet；整合后的独立审阅、完整检查及 CI
  由原交付协调者执行。

Refs: D238。
