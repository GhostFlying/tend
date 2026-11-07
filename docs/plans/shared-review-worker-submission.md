# 共享审阅结果的 worker 提交入口

设计依据：D186。正式审阅发现 `ResultReport.shared_review` 已被服务端读取，
但 worker 的 `submit_result` 仍发布拒绝该字段的严格 schema，真实 reviewer 无法合法提交。

1. 在共享 `ResultToolSpec` 的 report schema 中公开可选的结构化审阅结果，包含实际
   review、候选、需求摘要、仓库检查、verdict 与 findings；保留普通报告和文件入口。
2. 沿既有 Results handler、不可变报告及本机提交恢复传递原始结果，不新增提交协议、
   授权或审批。app-server 与 MCP bridge 继续使用同一工具定义。
3. 验证实际报告字段均被发布的严格 schema 接受，并由真实工具 handler 提交、保存和恢复。
   与本轮其余审阅修正一起进入最终完整检查、CI 和独立复核。
