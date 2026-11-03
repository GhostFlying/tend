# HTTPS事件流与页面读取复用

Refs: D23、D70、D165。

## 证据边界

现有HTTPS监听器没有HTTP/2的ALPN协商。长连接事件流在HTTP/1.1同源连接上占用一个
连接；这是一项可在本地真实TLS/HTTP传输复现的机制缺口，不等于已证明某次生产页面
加载失败的根因。原截图和外部API成功无法还原浏览器连接排队情况。

1. 使用既有本地TLS及真实HTTP客户端/浏览器构造受限同源槽与多个持久事件流，记录
   原版页面GET是否排队。所有地址为独立loopback，不访问tailnet、生产、模型或凭据。
2. 仅为正式HTTPS监听显式协商h2与http/1.1，保留当前证书获取、HTTPS启用检查、
   身份认证与明文HTTP监听行为。复用标准库，不引入新的第三方module。
3. 对相同持久流负载验证HTTP/2协商和GET可并发返回；HTTP/1.1-only客户端仍可用。
   验证证书与TLS失败不被降级成不安全HTTP，不在测试中连接实际tsnet。
4. 如果本地真实链路不能证明修复效果，则不提交推测性产品改动。若通过，独立小PR
   运行相称传输回归、规定review与既有门槛。真实用户页面恢复仍需fresh部署后的浏览器
   验证，不能把本地条件复现冒作生产因果。

## 必要 CI 回归同步修正

既有 event upload 日志回归在 AckEvents 清空 journal 后立刻制造第二次故障，
但这不能证明原 flushEvents 已返回并重置内存 outage 标志。原失败保留，不以重跑掩盖。
用标准库 synctest 与同步 HTTP transport 在每轮结束等到 uploader 真正静止于下一轮
定时等待，再触发下一次 outage；真实 journal、client 编解码及每次 outage 多次失败仍覆盖。
仅修测试同步，不改变生产日志、flush、runner 或 TLS 行为。
