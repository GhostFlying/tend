# 部署身份读路径交接与验证计划

1. 只读核验旧检查最终状态、日志、manifest、源码与 diff；两次取样一致后在当前任务 scratch 冻结五文件副本，记录 SHA-256、来源和时间。旧执行 unknown 与控制 pause 分开记录，不写旧树。
2. 仅在当前独立工作树还原冻结五文件，保留已有 D113 身份初始化与读池方案；不增加缓存、修改池或超时。
3. 用真实短私有临时目录复现 runner repository 控制测试，验证长路径 locator 与测试直连路径的差异。只修实际证据支持的最小问题，保留发布、幂等和拒绝第二 writer 的断言。
4. 运行对应 race 用例及身份并发与持锁回归；完整检查前向 owner 确认唯一 heavy 窗口。完整 make check 必须真实 exit 0。
5. 提交前明确旧 producer 的交接与控制边界；保留既有 Git 身份。独立 Codex review、fresh main、PR、当前 head CI 与正式验收分别取证，线性合入按授权执行。
6. 全程不执行生产操作，不将本地测试等同于 PWA 恢复或部署证明；失败和 unknown 证据保留在报告。

## 部署身份补丁主线整合计划

1. 核验同一 PR 的远端 head、最新 main、原实施 run finished 及已有用户 Git 身份；仅当前独立树拥有后续写入职责。
2. 阅读旧补丁七文件与主线并发能力增量，将旧提交 rebase 到最新 main；冲突仅保留必要兼容，逐文件比较旧补丁，不改缓存、池、超时或其他产品。
3. 保留旧完整检查与失败历史；纯重放无行为变化时只执行身份和长路径 socket locator 的有界定向 race 回归，最终组合完整检查由 fresh CI 执行，不另占本轮本地 heavy 窗口。
4. 固定最终 base、head、diff SHA-256，执行独立 Codex GPT-6.1-Sol/xhigh review；发现真实缺陷才最小修复及复核，审查后复核内容不漂移。
5. 以精确旧远端 head 的 force-with-lease 更新同一 PR，核验最终 head/base 组合 CI、全部必需检查与 mergeability，交付独立正式验收。
6. 不修改原冻结产物或其他任务工作区，不合入、不生产；父 P0 待独立合入、受控上线与 live proof。
