# Orca TraeX 显式启动参数

## 合同

沿既有 Orca Trae TUI 路径传递调用者选择的模型与 reasoning effort，不省略或替代值。复用现有 session-option catalog、参数去冲突及跨平台 shell 编码。TraeX 的实际 catalog ID 与 display name 分开；配置支持不是模型调用可用性证明。

旧 host 与旧 CLI 不能被认为支持：沿既有 status JSON 分别报告 client 与 runtime capability，调用者同时核对；新 CLI 的真实 launch 在 host 不支持时先拒绝。无新 provider 或 model invocation。

## 实施

1. 增加 Trae catalog，映射已核 CLI 参数及 effort 配置键；沿原 model preference validator 拒绝不支持的 effort。
2. 在既有 launch capability 集合增加 Trae 显式选择能力，status client 单独发布本地支持；真实 CLI launch 核 host capability。
3. 回归 exact model/high/xhigh、冲突参数与 prompt delimiter、缺失 host capability；定向测试及既有 changed-check。

## 发布边界

修改从当前正式 fork 基线开始，经现有独立 review 与 fork-hotfix 门禁；原 checkout/分支保持。部署与 named registry 配置由唯一 operator 在兼容版本发布后执行。
