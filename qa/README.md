# Upstream Pilot 验证手册

```bash
make test
PILOT_TEST_DATABASE_URL='<isolated PostgreSQL URL>' make integration
```

集成测试需要有创建 schema 权限的专用数据库，每个用例只清理自己创建的 schema。正式竞态验收可运行：

```bash
PILOT_TEST_DATABASE_URL='<isolated PostgreSQL URL>' go test -race -count=1 ./...
```

`make demo-upstream` 启动本机模拟器，默认 `127.0.0.1:33888`，公开测试 Key 为 `test-admin-key`。它不连接真实供应商。

```bash
curl -X POST http://127.0.0.1:33888/control/101 -H 'Content-Type: application/json' -d '{"probe_success":false}'
curl -X POST http://127.0.0.1:33888/control/101 -H 'Content-Type: application/json' -d '{"probe_success":true,"probe_delay_ms":1500,"billing_rate":2}'
```

建议覆盖：默认观察、降级与恢复、旧意图失效、部分写回、事务回滚、来源更换、证据过期、共享组故障、真实请求延迟回退及容量还原。模拟器不实现 Sub2API 的全部加权/粘性调度行为。

截图与运行数据属于忽略的 `output/`、`.local/` 和 `.playwright-cli/`。文档使用截图时只采用本项目生成的合成数据画面。


## 真实请求契约回归

`internal/upstream/traffic_feeds_test.go` 使用独立编写的合成记录模拟三类管理接口，覆盖上游 503 被备用接替、最终 429、原生额度分类、重复记录、缺失总数和部分接口拒绝访问。`internal/app/operational_readiness_test.go` 在隔离 schema 中验证分组备用时效、换源隔离与声明覆盖；`internal/quality/traffic_coverage_test.go` 验证不完整成功证据不能解除风险。

这些测试不调用真实供应商、不使用生产 Key。浏览器合成状态用于界面检查，原生 Sub2API 请求路径仍须另外验收。
