# 部署 Upstream Pilot

## 配置

从 `.env.example` 复制配置，至少设置：

| 变量 | 用途 |
| --- | --- |
| `PILOT_DATABASE_URL` | 独立 PostgreSQL 数据库 |
| `PILOT_MASTER_KEY` | Base64 编码的 32 字节加密主密钥 |
| `PILOT_LISTEN_ADDR` | 默认 `127.0.0.1:33777` |
| `PILOT_PUBLIC_URL` | 用户访问地址 |
| `PILOT_COOKIE_SECURE` | HTTPS 部署设为 `true` |
| `PILOT_LOG_DIR` | 可写审计目录 |
| `PILOT_WORKERS` | 后台并发任务数量，默认 8 |
| `PILOT_ALLOW_PRIVATE_UPSTREAMS` | 是否允许连接可信内网，默认关闭 |

## systemd

为服务创建 `upstream-pilot` 用户和组，将解压后的可执行文件安装为 `/opt/upstream-pilot/upstream-pilot`，配置文件放入 `/etc/upstream-pilot.env` 并限制为服务身份可读。复制 `deploy/upstream-pilot.service` 后执行：

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now upstream-pilot
sudo systemctl status upstream-pilot
```

先在受信任的本地连接中初始化管理员，再通过 HTTPS 反向代理提供访问。健康端点是 `/healthz` 和 `/readyz`；它们仅证明进程和数据库可达，不证明上游可用。

## 升级与回滚

升级前备份数据库、主密钥和审计日志；保留旧二进制。应用启动会执行尚未应用的迁移。检查目标版本、数据库迁移、健康端点、实际界面和代表性请求。存在迁移时，回滚不能只替换可执行文件，应先核对旧版本的数据库兼容性。

此前本地开发版本的配置前缀和可执行文件名已统一为 `PILOT_` 与 `upstream-pilot`；请更新启动环境并重新登录。身份迁移仅转换旧密码哈希标识，bcrypt 内容和密码材料不变。数据库与加密主密钥必须原样保留。


## 升级消息中心（迁移 033）

先停止所有 Pilot 实例并备份 Pilot 自己的数据库和主密钥，再启动新版本执行迁移；不需要停 Sub2API。迁移保留旧质量与余额接收地址、启用状态和各自订阅范围，并将旧配置表改名为 `_legacy`。检查消息中心渠道及规则后再启用新的飞书渠道。不要仅替换旧二进制回滚，旧版本仍依赖原表名；回滚须使用升级前的数据库备份。

新建渠道默认停用，不回放历史事件。测试连接会向选中渠道发送消息，正式部署时应使用操作员确认的机器人配置。[完整规则与兼容说明](NOTIFICATIONS.md)。
