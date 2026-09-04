# 架构

Upstream Pilot 由 Go API/后台任务、PostgreSQL 状态存储、React 控制台组成。前端构建产物嵌入 Go 可执行文件。

```mermaid
flowchart LR
  UI[运营控制台] --> API[Go API]
  API --> DB[(PostgreSQL)]
  Collector[探测 / 请求 / 余额 / 采购倍率] --> DB
  DB --> Policy[独立风险评估]
  Policy --> Planner[分组与模型排序]
  Planner --> Control[意图 / 读回 / 写回确认]
  Control --> Admin[Sub2API 管理接口]
  Control --> DB
  DB --> Notify[通知队列]
```

## 代码边界

- `cmd/upstream-pilot`：配置、迁移、HTTP 服务和后台任务生命周期。
- `internal/quality`：风险状态和确定性的候选排序。
- `internal/app/engine_*`：证据有效性、共享分组组件、待办核对及受控写回。
- `internal/upstream`：Sub2API/NewAPI 客户端、流式解析与外部 HTTP 限制。
- `internal/database/migrations`：顺序迁移，启动时以数据库锁串行执行。
- `internal/auditlog`：按所有者存储的审计日志。
- `web/src`：认证、质量、账号、分组、通知和管理界面。

账号是上游凭据/端点，分组是候选池。共享账号连接多个组时，无法同时满足的顺序会产生冲突，而不是由执行顺序决定结果。

## 一轮决策

加载账号与证据，读取远端当前值，生成组内顺序，然后按相关组件执行。写回前记录意图；远端结果与本地事务不能合并为原子事务，因此中断后需先确认已执行字段、撤销未执行目标，再重新计划。失败隔离到有共享依赖的组件。

风险分为失败、慢请求、余额和价格，各自保存证据与恢复计数。快照使用时重新检查时间；不能以采集器上一次成功的布尔标记无限延长有效期。真实首字样本足量且未过期时优先用于排序，否则回退到有效探测数据。

## 仍需注意

本项目不是请求代理，也不能制造供应商容量。上游接口能力和字段会随版本变化。来源更换、历史兼容字段以及大库存下的查询成本仍存在已知审查事项，见 `REVIEW.md`。
