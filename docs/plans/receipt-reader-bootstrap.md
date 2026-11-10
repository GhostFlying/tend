# 只读回执传输的原线程启动

## 实证问题

普通 owner 协调 action 已接受的 native_owner_tools 是原线程 binding 身份的一部分。
自动派生 generic receipt_only action 时该位未保留，选择器将缺失投为 false，导致
新只读传输找不到原线程 binding。启动又允许 StartThread 兜底，被正确的业务 fence
拒绝，反复消耗同一原输入的启动预算。严格 reader 的现有测试未覆盖此普通回收路径。

## 实施步骤

1. 在改产品前保存真实 fixture RED：普通 native-owner Run 已接受原 mode、完成物理
   停止但留有历史事件，自动派生 generic reader；新生成与已有缺字段 action 都必须
   保留原完整 pin、原 binding 和历史。线程层覆盖普通 reader 的旧工具集、拒绝 resume
   及缺失原 binding，禁止新线程/分叉/迁移工具集。
2. 生成只读 action 只表达 receipt_only；不在普通关闭、Issue 刷新或全局 Claim
   的事务中解析历史执行 pin。实际准备该 reader 的 selection/attachment 时，
   统一从同 manager Task 原 action/冻结执行记录恢复已接受身份，不改历史
   action/Run，不从当前可用性或配置猜 mode。旧 action 缺字段时可读取同 Task、
   原 Run、原 Issue/epoch 的已准入执行快照，保留其原 false 默认语义，仍核对原
   完整执行 pin。缺少可信原身份或历史原件损坏时，由本项实际附着如实返回未知
   或错误，不给无需历史证明的普通 Close 增加前置条件。
3. 所有 receipt_only 启动都只 Resume 原线程，不 Start/Fork、不做工具集迁移。
   RequireReceiptReader 原证明与模型恢复边界保持；generic reader 不被追加强模式
   的新必填事实或协议能力。此修复约束内部只读启动，不修改通信请求格式或旧有效
   回执的准入语义，无需协议版本变化。
4. 保留原业务 fence、请求与事件身份、原输入、Task/spec/pin、历史回执；不重发业务，
   不新增权限、协议、schema 或配置。真实就绪后才按既有回执路径继续。
5. 运行必要定向 regular/race/vet，保存原生 JSON 与失败/通过证据。最终设计 D213、
   完整检查、独立审阅、CI、提交和部署由协调者统一处理。

## 完整检查回归修正

完整检查的 `TestClosedManagerRepairSkipsUnneededHistoryProof/ordinary_close`
证明最初的生成期 mode 补全过早读取已应用事实的历史 pin；损坏的历史 JSON
因此使本不需要该证明的普通 Close 回滚。取消生成期解析，保留实际 attachment
的完整验证。回归同时覆盖普通 Close 跳过无关历史、实际新 reader 和已落盘
reader 在读取时保持原 binding、完整 pin 和只读 fence；不吞掉真实读取错误。
