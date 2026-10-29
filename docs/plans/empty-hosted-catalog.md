# 未配置托管 Agent 的只读目录合同修正

Refs: D121、D123。可选托管配置不存在时，owner 目录读取应成功返回 `state=unconfigured`，投影格式版本为 1、source_kind 为 inline，Agent/runtime 数组为空；不伪造环境、配置版本、主 Agent、请求或 hash。API 仅将 store.ErrNotFound 映射为空态，鉴权先行，真实存储错误仍按原内部错误处理，已配置投影不变。App/store 写入与配置登记不改。

目录在原托管分组显示一行“尚未配置托管Agent”，新建 Issue 自然保留 owner 选项。保持已批准布局。实际 API 覆盖空配置、已配置原结果、未鉴权及真实 SQL 错误；沿原浏览器 fixture 覆盖空目录、owner 新建表单与已配置目录，运行目标 race/vet 和 Web 检查。旧完整检查独立保留，本片不启动额外 App 全检；完整复检由协调者在旧检查自然完成后于原物理路径复用缓存。
