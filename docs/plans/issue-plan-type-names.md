# Issue 计划公共类型名：B0

依据D121，将原计划/证据结构声明直接改为Issue*，保持单一模型与现存储/消息语义。本片仅类型名称收口；活动owner JSON映射、会话投影、旧API/tool退役与可见页面属于后续独立切换。

## 边界与步骤

1. 直接改7个原声明：WorkItemPlan/Stage/FollowOn/Delivery/Evidence/Closure/TaskDisposition→IssuePlan/Stage/FollowOn/Delivery/Evidence/Closure/TaskDisposition。7个嵌套enum类型与Values函数同样改Issue*，literal值/顺序不变；不增alias、复制模型或转换层。
2. 全部生产/测试引用与匿名嵌入字段机械同步；JSON tags/omitempty、层级/数组、请求与枚举值、SQL/迁移、不可变snapshot、opaque ID及runner1/0字段保持。仅schema定义/引用和生成TS类型名变化。保留旧活动入口，不能把Go类型改名当已完成其退役。
3. 正常生成API Schema/TS。手写Web仅type import/annotation同步；包括既有Details.tsx，不改render/文案/布局/交互，以type-erased JS逐字节相同证明。任何超出类型适配的改动不纳入本片。
4. 验证现enum/schema/工具导出、存储roundtrip/冻结hash、实际runner1/0消息；相同输入Marshal输出保持原JSON。运行必要compile/vet/staticcheck、定向race与TS/type-only等价，不为每字段增加镜像测试。正式全检与规定独立review在后续集成安排。

候选以已冻结canonical caller A r2及非可见Web数据链为前件，主协调者管理的hosted恢复修正独立组合，不能据本片声称前件已正式纳入或发布。没有部署/真实数据操作，无UI设计或可见页面实施。
