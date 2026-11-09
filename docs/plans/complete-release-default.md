# 普通升级默认完整发布集

依据：D200。用户确认线上不存在历史部署。新建普通升级只接纳 format 2 完整发布集，不提供旧包开关或旧部署适配分支；现存账本与已派发操作仍按原身份读取和恢复。

## 实施

1. CLI 的 `upgrade start` 缺省由 server 本机协调器选择官方最新已发布的 main fleet 包；`--commit` 固定完整源码提交，`--release` 指定本地完整包。三种方式共用现有 request ID、授权、维护和健康流程。
2. 官方选择使用固定仓库的公开 release feed 和 SHA256SUMS，包含 prerelease，排除 draft，不依赖 API 配额或新增主机凭据；固定完整 main 标签、发布集与 fleet 资产摘要。先在私有目录原子持久化请求选择，再下载、校验和暂存。原 request 重试只恢复原选择，拒绝参数变更，不自动重发业务写请求。
3. 新升级入口验证 format 2 和全部组件；缺少发布集、旧格式、损坏组件及源码身份不符均拒绝。内部读取及已存在批次 Tick/retry/recovery 保持原合同；不修改 runner N/N−1 wire。
4. 更新 CLI 帮助、能力目录、操作文档及 AGENTS.md，明确新部署统一完整发布集，原生 service manifest 仍为 format 1。自维护的独立组件不纳入普通业务升级门槛。

## 验证与交付

- 固定 HTTP fixture 覆盖默认选择、完整 commit、未发布/draft/不完整资产拒绝、摘要与归属冲突、安全解包、下载失败、重启与并发选择固定；使用可控的本地数据，不联网。
- 新升级拒绝 service-only 和 format 1，合法完整包沿正式准入；原旧请求与历史恢复不重新选包，不增加业务权限或重复操作。
- 聚焦回归、最终候选完整 make check、独立 review、required CI、最新主线 rebase 后经 PR 合入。本轮不部署。
