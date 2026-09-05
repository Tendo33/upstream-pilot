# Upstream Pilot · 上游领航

**让上游的速度、可用性与成本变化可见，并把调整落到分组。**

Upstream Pilot 是 Tendo33 维护的 Sub2API 上游运营控制台。它同步账号与分组，采集探测、真实请求、余额和采购倍率，生成可解释的调度建议，并在明确启用后调整优先级与容量。

[English](README.en.md) · [使用手册](docs/OPERATIONS.md) · [消息中心](docs/NOTIFICATIONS.md) · [部署](docs/DEPLOYMENT.md) · [架构](docs/ARCHITECTURE.md) · [整仓审查](docs/REVIEW.md) · [辅助能力改进路线](docs/SUB2API_COMPANION_REVIEW.md)

> 当前为预览版本，默认只观察。已知问题与验证边界以整仓审查为准，不能将本地模拟器通过等同于生产 SLA。

![Upstream Pilot 本地模拟数据界面](docs/assets/quality.png)

## 能做什么

- **看质量**：流式首字、请求耗时、错误类型、余额、采购倍率及历史变化。
- **排顺序**：按分组和测试模型选择价格优先、速度优先或均衡，健康候选优先于降级候选。
- **控动作**：优先级、停调、并发上限、负载系数分别控制，发现人工修改时停止覆盖。
- **收告警**：飞书、企业微信和通用 Webhook 共用消息中心，按渠道订阅、去重、重试并查看业务回执。
- **保留证据**：独立风险、恢复确认、数据时效、待确认写回、决策历史和通知投递状态。
- **兼容上游**：对接 Sub2API 管理接口，并支持 NewAPI 来源的成本和余额信息。
- **验证入口**：按模型和协议建立分组或账号档案，验证流式、工具结构、完整结束和预算。
- **核对冗余**：登记供应商、失效域和共享额度池，按已确认的独立来源计算备用。
- **统一成本**：按币种、token 单位、缓存价格、充值比例和真实用量结构估算采购成本与余额续航。
- **看自身健康**：查看批量采集、任务租约、积压、失败、数据库和进程状态。

请求代理、会话绑定、请求内重试与实际流量选择由 Sub2API 执行。缺少运行约束、最终请求结果或共同计价基础时，Pilot 会保持未知，不把账号数量、日志行数或倍率直接当作服务冗余与实际采购成本。

## 本地启动

需要 **Go 1.26+、Node.js 20+ 和 PostgreSQL 14+**。

```bash
git clone https://github.com/Tendo33/upstream-pilot.git
cd upstream-pilot
cp .env.example .env
openssl rand -base64 32
```

编辑 `.env`，填入独立数据库的 `PILOT_DATABASE_URL`，将生成的密钥填入 `PILOT_MASTER_KEY`，然后：

```bash
make build
./scripts/run-local.sh .env
```

默认仅监听 `127.0.0.1:33777`。打开浏览器创建管理员，再添加 Sub2API 站点。主密钥用于解密保存的管理凭据，需要与数据库一起备份。

## 从观察到控制

1. 添加站点并同步库存，为账号选择实际支持的模型。
2. 配置探测和采购倍率采集，先观察足够的有效样本。
3. 在分组策略中选择价格、速度或均衡，检查建议与冲突。
4. 按账号显式开启自动优先级；停调与容量调整需要分别开启。
5. 在质量页查看接管状态。还原参数和停止接管有明确入口，调度开关单独管理。

采购倍率代表成本，不会随着质量规则改写用户售价。分组页的售价倍率规则是单独的手动操作。

## 验证与模拟

```bash
make test
PILOT_TEST_DATABASE_URL='postgres://USER:PASSWORD@127.0.0.1:5432/TEST_DB?sslmode=disable' make integration
make demo-upstream
```

数据库测试创建并清理自己的随机 schema。未提供测试数据库时，集成用例会跳过。模拟器只监听本机，使用公开测试 Key `test-admin-key`，不连接真实供应商。详见 [QA 手册](qa/README.md)。

## 发布

```bash
RELEASE_VERSION=0.2.0-preview.1 make release
```

输出 `dist/upstream-pilot-linux-amd64`、SHA-256 文件和包含许可证的压缩包。ARM64 构建使用 `TARGET_ARCH=arm64`。systemd 示例位于 `deploy/upstream-pilot.service`。

## 来源与许可证

Upstream Pilot 是**在 MIT 许可的 S2AM-GO 底座上持续改造的独立维护项目，并非完全原创重写**。认证、管理、采集与部分界面仍保留派生实现；质量引擎等扩展由本项目独立实现。没有导入 Guardian 源码。

原作者署名保留在 [LICENSE](LICENSE)。[来源审计](docs/PROVENANCE.md) 和 [第三方说明](THIRD_PARTY_NOTICES.md) 说明了代码底座、第三方依赖与统计口径。
