# CLI owner 写入只提交一次

Refs: D11、D142。

## 已证问题与范围

`sendEnvelope` 对一般 transport error 自动再次 POST；这类错误也包括服务端已接受请求但回执断开，无法证明请求未送达。保留同一 request ID 不等于已经对账。现有 typed 写入、Issue replay 与 request replay 共用该入口。注册仓库、分类写入与并发配置还复制了相同的自动重发块，一并直接复用该共享入口，不改变业务解析或结果展示。

## 实施步骤

1. 用真实 HTTP 测试服务器接受原 Envelope 后断开回执，证明单次 CLI 调用只能 POST 一次，并能用原 request ID 读取已保存结果。
2. 去掉 `sendEnvelope` 的自动第二次 POST；返回带原 request ID 和 `request show` 指引的 unknown outcome。调用者保留原 Envelope，显式对账后才决定原身份 replay。
3. 更新受影响的既有测试和恢复说明；正常成功、业务拒绝与只读 GET 能力不变，不新增 flag，不修改 HTTP/2 传输实现。
4. 运行有限 CLI race 回归并构建本地 CLI。独立提交交由当前可靠性批次集成；不单独创建 PR 或运行全仓检查，正式最终版本门槛由集成者负责。

## 边界

不修改服务端、请求去重、CAS、业务决策或生产状态，不把传输未知当作已失败或未提交。测试仅使用本地替身，不重放真实 owner 请求。既有源码身份和请求 Envelope 保持不变。

## 真实 HTTP 恢复回归对齐

最终集成的 API 检查暴露既有真实 HTTP fixture 仍要求同一次调用自动发送两次 POST，并连续丢弃两次回执。这与单次提交合同相冲突。保留该失败作为 RED，仅更新既有 fixture：服务端提交后丢弃一次回执，首次断言恰一次 POST、一个 Issue 与原身份的 unknown/查询指引；用原 ID 查询确认已提交，再显式重放冻结原 Envelope，校验总 POST 数、请求身份、原字节和 payload hash、Issue 去重。原有 changed-payload 拒绝、控制版本读取及无额外本地账本覆盖继续保留。

本增量不修改产品代码、超时或生产状态；先运行该恢复 case 与相邻真实 HTTP 去重恢复 race，交由原 CLI 作者独立只读核对测试差异。完整最终版本检查与合入门槛仍保持。
