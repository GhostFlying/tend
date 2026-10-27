# 补充消息回执恢复与历史认证（编码前）

## 调查结论

已有 D116 的专用消息 journal 保留 claim、invoked、report 和 settled，不会被 Done 删除，但 Runs/hello 排除 done。执行阶段才恢复消息，最终报告未排空，因此完成后已有 accepted 回执可能永远不上报。

ReportRunMessage 已严格验证 run、role 和 message/receiver/thread/turn，允许 unknown 转为真实迟到事实；runner HTTP 的普通 RunCredential 拒绝 released lease，导致该路径不可达。不能放松普通 run 认证，否则恢复旧 run 的任务写入权限。

指定 D107/D108 已线性整合；D116 保留原补充消息设计，handback 同时识别带消息提示和不带提示的历史正文。

## 实施计划

- R1：worker_done/submitting/releasing 先验证原 worker 停止，再于最终报告前排空专用回执；上传网络故障不能阻塞本机停止与 fencing。增加不依赖 hello/worker 的最终阶段消息恢复循环，读取包括 done 的专用 journal 责任，逐 run 有界恢复。已 invoked 的恢复直接使用冻结 claim 身份报告 unknown，不重新调用原执行器；已保存 report 沿原 request_id 重放。待回执计入维护 pending operations。
- R2：仅 runner RPC 的 report_message 可在普通认证失败后尝试历史回执认证；继续验证 token、当前 host/node 和原 role，并以原 run 与完整领取身份匹配历史消息。其他 RPC 和 GET 保持原认证。回执经原 app 去重与单事务审计，不恢复执行/控制/claim。
- R3：回归覆盖全部最终阶段、回执响应丢失、完成时重启、done 后恢复、保留原报告 ID、未重启执行器；公开 HTTP 测试覆盖 completion/cancellation 后正例、重复请求与身份/路由/正文越权反例，并核对 task/run 不变。
- R4：更新 D116 文档，运行 targeted race、真实 Orca/app-server 的隔离 integration、make check 与跨平台编译，按仓库命令独立 review，处理发现后本地提交与报告。

## 界限

仅扩大既有回执事实的认证可达性，不改旧有效请求含义，不改变 runner 协议版本。没有接收回执仍为 unknown，不以服务端持久化或 HTTP 成功宣称模型消费。不部署、不推送、不建 PR，不干预既有 worker。
