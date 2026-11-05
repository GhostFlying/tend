# 自然重启后的维护来源身份

Refs: D155。基于已审查的 manager 生命周期日志候选，行为修复独立提交。

## 已确认问题

同版本受监督重启保留原 committed Binding，并在核实新进程后更新
CommittedProof.Process。现有 receipt 分别表达历史提交 Binding 与当前进程身份。
普通维护计划也分别读取这两项，但其来源校验再次要求两个 launch ID 相同，
使正常重启后的已提交服务无法进入下一次普通维护。

## 范围与不变量

- 不刷新或重写原 Binding、历史 maintenance ledger、receipt 或原请求。
- 普通无 parent 维护继续要求计划来源精确等于当前持久 committed proof，
  Current/Version 和当前 Process 也必须一致。历史 Binding 的 launch 不再被
  当成当前 kernel launch；有效进程身份、artifact hash、实际 Inspect 及唯一 Find
  校验全部保留。
- failed-parent 前滚仍要求历史 successor Binding 与 parent Process 的 launch
  相同，已有 owned launch-exit 分支保持原规则。
- 新 successor 不得复用历史 Binding launch 或当前 Process launch。
- 不增加接口、wire、迁移或手工 proof/state 修改入口，不改重启行为或业务运行。

## 实施与验证

1. 使用现有 updater 故障替身，真实经过两次 monitor/start 同版本重启；证明原
   Binding 保留、当前 Process 改变、下一维护采用默认来源时被原校验拒绝。
2. 仅修普通维护的来源谓词，保留 failed-parent 规则和新 successor 的独立身份。
3. 回归下一维护接受且历史 proof 不改、伪造 Binding/当前进程、实际身份变化、
   同 launch 多进程、两种 successor launch 复用及 failed-parent 不匹配拒绝。
4. 必要定向 race 和本地 updater 构建先行，冻结 exact head/tree 与产物哈希供
   独立核查。完整检查遵守唯一重负载执行安排；规定 review 与 CI 并行。

## 安装边界

实现只准备可审查源码和本地候选。安装须沿既有独立 installer、manager/锁与
当前 kernel 身份核验链接管同一 server，不停止或重启 server/runner。
旧运行 updater 不理解新谓词时，不能通过编辑其 state、旧计划或伪造 proof 绕过；
应先验证已有 handoff 对新 updater 字节和原进程的无中断安装路线。
生产操作由明确的唯一操作者另行执行，本实现不发生产控制请求。
