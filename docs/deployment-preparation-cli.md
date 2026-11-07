# Issue 部署准备读取

`tend issue deployment-preparation <issue_id> [--json]` 读取该 Issue 的部署准备事实，默认输出完整 JSON。`--json=false` 仍完整展示全部字段。

返回值的 `preparation` 包含服务端公开固定实例、当前完整活动 runner 目标集合、各目标的运行状态与控制版本、安装回执、已结清基线、未结清升级及该 Issue 已登记的 provider 事实。每份事实保留来源、观察时间、事件水位及对应版本。运行心跳、进程观察、安装完成回执分别展示；缺失或无法确认的信息保持 `unknown`、`missing` 和相应原因，不能从心跳推断部署完成。

该命令只需要 Issue ID，不接受 host 或 provider 选择器。它不创建请求信封、不刷新 provider、不安排升级、不取得目标锁，也不授予部署权限。读取中断后可重复同一读取，无需写请求 ID 或控制版本。

owner API 提供同源的 `GET /api/v1/issues/{id}/deployment-preparation`。Main 与 Issue 负责人使用 `issues_deployment_preparation_read`：负责人省略 `issue_id` 时读取当前 Issue，显式选择仅限本 Issue 及其后代；Main 显式选择 Issue。读取不会扩大该 Issue 可见的 provider 范围。

Refs: D142, D189。
