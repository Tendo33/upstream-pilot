# S2AM-GO

<p align="center">
  <strong>简体中文</strong> · <a href="./README.en.md">English</a>
</p>

S2AM-GO 是面向 Sub2API 的轻量账号调度与运维控制台。后端使用 Go，前端使用 React/Vite 并嵌入同一个二进制；运行时只依赖 PostgreSQL。默认监听端口为 `33777`。

这个版本有意删除旧版的“智能调度”：不会计算或改写 `concurrency`、`load_factor` 等容量参数。系统只处理账号探活与自动停用/恢复、上游余额读取、账号倍率回写、按倍率排序、可选的近期缓存率加权、分组倍率保护、分组倍率跟踪，以及这些能力的账号级开关。

> S2AM-GO 会通过 Sub2API Admin API 改写真实账号的 `schedulable`、`rate_multiplier` 和 `priority`。首次启用自动化前，应先在测试账号上验证站点版本、权限和倍率配置。

## 项目截图

### 运行总览

![S2AM-GO 运行总览](docs/screenshots/overview.png)

### 账号管理与余额

![S2AM-GO 账号管理与余额](docs/screenshots/accounts.png)

## 功能

### 定时探活与停调恢复

- 每个账号独立开关，可设置探测间隔、超时、失败阈值、恢复成功阈值和测试模型。
- 探测调用 Sub2API 的账号测试接口；网络错误、上游错误和超时都计为失败。
- 连续失败达到阈值时，仅当账号当前仍可调度，S2AM-GO 才将其设为 `schedulable=false` 并记录为本系统托管的暂停。
- 托管暂停后的账号会继续按间隔探测；连续成功达到恢复成功阈值后会清零计数并恢复 `schedulable=true`。默认阈值为 `1`，与旧版本行为一致；期间任意一次失败都会清零连续成功计数。
- 原本已由人工暂停的账号不会被 S2AM-GO 认领，因此不会在探测成功时被意外启用。
- 关闭普通探活后，已经由系统暂停的账号仍会继续恢复探测，直至成功，避免账号永久卡在托管暂停状态。

账号页的“调度”开关表达人工调度意图，与健康探测开关相互独立。人工关闭会写入 `schedulable=false` 并清除 `managed_hold`，后续探测即使成功也不会自行开启；探测失败达到阈值造成的暂停则保持人工意图为开启，以 `managed_hold=true` 标记为 S2AM-GO 托管，探测成功后才自动恢复。人工重新开启或关闭调度都会结束此前的托管暂停归属。

失败探测会保留上游 HTTP 状态（能够取得时），并归类为稳定的 API 枚举：

| 分类 | 含义 |
| --- | --- |
| `AUTH` | 凭据无效、过期或无权访问 |
| `BALANCE` | 余额、额度或配额耗尽 |
| `RATE_LIMIT` | 请求限流或模型冷却 |
| `UPSTREAM` | 上游服务、网关或连接异常 |
| `TIMEOUT` | 请求超时或 deadline exceeded |
| `CONFIGURATION` | 模型不存在、请求无效或配置不兼容 |
| `UNKNOWN` | 证据不足，无法归入以上类别 |

分类时先识别错误正文的语义，再使用 HTTP 状态兜底，因此即使余额不足被上游包装为 `401`、`403` 或 `429`，仍会归为 `BALANCE`。账号页显示分类及 HTTP 状态；最近一次失败为 `BALANCE` 的账号会使用淡红底色和红色左侧标识。探测成功后会清除最近失败分类和状态。

账号页的 Uptime 按最近 `60` 次探测计算，定时探测和手工“立即测活”都计入样本；它不是固定时长的时间窗口。百分比为窗口内成功次数除以实际样本数，界面同时显示成功数/样本数、从旧到新的成功/失败时间线及窗口起止时间。未发生过探测时百分比为暂无、计数为 `0/0`。

### 倍率同步

倍率同步同样按账号独立开关。S2AM-GO 支持两类倍率源：

- `sub2api`：调用托管站点的 `/api/v1/admin/accounts/{id}/upstream-billing-probe`，读取 `effective_rate_multiplier`。
- `newapi`：直接访问源站，优先读取 `/api/user/self/groups`；仅在端点不受支持时，先验证身份再回退 `/api/pricing`。支持 Bearer/PAT 和完整 Cookie，并可附带 `New-Api-User`。

写回 Sub2API 账号的有效倍率为：

```text
effective_rate = source_rate / recharge_ratio
```

例如源站分组倍率为 `2.0`、充值换算比为 `2.0`，写回账号的成本倍率为 `1.0`。写入后会重新读取返回值校验；失败不会用默认值覆盖已有倍率。

开启 NewAPI 倍率同步前必须显式绑定 `source_group`，避免把用户当前分组或同名账号猜测成实际成本分组。设置页可以用尚未保存的地址和凭据预览源站分组，再选择准确分组。凭据使用 AES-256-GCM 加密后存入 PostgreSQL，API 不回显明文。

每个 NewAPI 账号会维护独立的源站凭据状态：`unknown` 表示尚未验证或源站地址、凭据、用户 ID 等配置刚发生变化，`valid` 表示最近一次读取分组或倍率同步已通过认证，`invalid` 表示源站明确拒绝了凭据。只有 NewAPI 返回 `401/403`，或响应明确表达 Session、Token、登录状态或凭据已过期/无效，才会写入 `invalid`；普通网络错误、超时、服务端故障和分组缺失不会被误判为凭据过期，也不会覆盖已有状态。账号页会对 `invalid` 账号显示红色底色和“NewAPI 凭据失效”标识，并在页面顶部显示当前租户的失效账号总数。更新凭据后状态先回到 `unknown`，下一次读取分组或倍率同步成功时恢复为 `valid`，告警随之消失。

库存同步会把 Sub2API 账号资料中观察到的上游地址保存为只读的 `observed_source_base_url`；NewAPI 设置中由用户确认或修改的地址保存为独立的 `source_base_url`。观察值后续变化不会覆盖用户值，切换到 NewAPI 时界面会在用户值为空的情况下用观察值预填，但输入框仍可编辑。余额快照按规范化 URL 与访问 key 的组合分组，同 URL 不同 key 会作为不同账号处理；数据库仅保存 key 的单向指纹，不保存额外的明文副本。账号名称会优先使用用户配置值、其次使用观察值作为源站外链，并在新标签页打开；没有可用地址时只显示文本。

S2AM-GO 在库存同步时根据账号平台、类型、凭据元数据和账单探测标记区分 Sub2API/NewAPI；对仍无法判断且符合条件的 OpenAI API Key 上游，还会使用受限 HTTP 客户端检查源站 `/api/status`。无法确认时保留或使用 `sub2api`。在账号设置中手工选择来源类型会置 `source_type_locked=true`，后续库存同步不会再用自动分类覆盖该选择。

### 余额与版本提醒

账号页会批量读取当前列表中账号的上游余额。Sub2API 类型账号通过托管站点的账号导出接口仅提取 `base_url` 和 `api_key`，再以 Bearer 认证请求源站 `/v1/usage`；NewAPI 类型账号使用已加密保存的地址、凭据和可选 `New-Api-User`，优先读取订阅接口并回退到用户信息接口。界面显示剩余额度，并在响应可用时同时显示已用、总额和套餐名称。余额请求有独立的批量上限、并发和超时控制，原始凭据不会返回浏览器。

每个用户可以在“预警”页面设置一份作用于当前工作区全部账号的余额阈值、企业微信群机器人 webhook 和通知冷却周期。后台余额快照刷新后，会按上游返回的原始余额单位比较阈值，将所有低余额账号汇总为一条企业微信 `markdown` 消息。冷却窗口通过 PostgreSQL 原子领取，即使部署多个 S2AM-GO 实例，同一用户在冷却期内也只会触发一次自动通知。机器人 webhook 使用 AES-256-GCM 加密保存，API 不回显 URL 或机器人 key；设置页支持独立发送测试通知。

悬浮导航提供项目 GitHub 入口。账户菜单显示当前构建版本、commit 和构建时间；服务端通过 GitHub `releases/latest` 重定向检查最新 Release，标准语义版本高于当前版本时在 GitHub 图标上显示红点，并把版本行链接到最新 Release。成功检查缓存 6 小时，失败只做短期缓存且不会影响控制台加载；`dev` 或非标准版本不会误报更新。

### 分组倍率跟踪

顶部导航中的“分组”页面展示当前用户各 Sub2API 站点同步到的分组、当前倍率、成员数、绑定来源、规则状态和最近应用结果。分组倍率规则以一个已同步的 Sub2API 分组为写回目标；每个目标最多绑定当前用户拥有的 100 个账号，来源账号可以位于该用户的不同站点。计算使用来源账号当前写入 Sub2API 的 `rate_multiplier`，已删除、不可用或倍率为空的绑定不会参与计算。

| 模式 | 计算基值 |
| --- | --- |
| `first` | 绑定顺序中的第一个可用账号倍率 |
| `average` | 所有可用绑定账号倍率的算术平均值 |
| `min` | 最低可用绑定账号倍率 |
| `max` | 最高可用绑定账号倍率 |
| `custom` | 受限公式解析器的计算结果 |

`offset` 是 `-100000..100000` 范围内的固定正负偏移，始终在上述基值或自定义公式结果之后相加。自定义公式最多 500 个字符，不是 JavaScript，不支持属性访问、赋值或任意函数调用；语法如下：

- 运算符：`+`、`-`、`*`、`/`、`%`，支持一元正负号和括号。
- 汇总变量：`avg`、`first`、`current`、`count`，分别表示可用来源平均值、第一个来源、目标分组当前倍率和可用来源数量。
- 来源变量：`r0`、`r1` 等，或等价的 `rate(index)`；下标从 `0` 开始，按可用绑定顺序取值。
- 汇总函数：`min()`、`max()`、`sum()`；不传参数时使用全部可用来源，也可以显式传入表达式参数。
- 数学函数：`abs(value)`、`floor(value)`、`ceil(value)`、`round(value[, places])`、`clamp(value, min, max)`；`round` 的小数位范围为 `0..12`，省略时为 `4`。

例如 `round((r0 * 0.7) + (rate(1) * 0.3), 4)` 按 70%/30% 合并前两个可用来源；`clamp(max() + current, 0, 2) / count` 可以组合来源最大值、当前目标倍率和来源数量。系统最后再加 `offset`、四舍五入到 4 位小数，并要求结果位于 `(0, 100000]`。没有可用倍率、除以零或结果越界时不会写回，上次错误会显示在分组页并写入活动日志。

规则可“保存并应用”，也可通过分组页或 API 手动应用。启用规则后，任一绑定账号倍率同步成功，以及目标站点或任一来源站点完成库存同步时，系统都会自动重新计算并调用 Sub2API 分组更新接口；只有上游返回值确认已持久化后才更新本地快照。

### 全局优先级排序

Sub2API 的 `priority` 数值越小，实际调度优先级越高。S2AM-GO 对每个 Sub2API 站点分别建立全局排序平面：

1. 只选择已开启“倍率排序”且具有有效账号倍率的账号。
2. 默认按账号倍率从低到高排序。
3. 最低倍率使用站点的 `priority_start`，每增加一个不同倍率档位增加 `priority_step`。
4. 相同倍率得到相同优先级；远端账号 ID 仅用于稳定排序。

不同 Sub2API 实例拥有彼此独立的调度平面，因此“全局”指单个站点内的全部受管账号。没有倍率或关闭排序的账号会保留人工优先级。

站点可另外开启“缓存率排序”。开启后，S2AM-GO 会定期读取 Sub2API `/api/v1/admin/usage/stats` 的今日累计 token，并按站点配置的统计窗口（默认 1 小时，可改为 30 分钟或自定义秒数）计算账号缓存率：

```text
cache_rate = cache_read_tokens / (input_tokens + cache_creation_tokens + cache_read_tokens)
```

该公式与 Sub2API 渠道监控中的缓存率口径一致。窗口内没有新流量的账号不会被误判为高缓存；缺失样本会使用当前可排序账号的平均缓存率。排序分数为：

```text
score = rate_weight * rate + cache_weight * (1 - cache_rate)
```

分数越低越优先；相同分数仍得到相同优先级。默认两项权重都为 1 时，约 1.0 的倍率差距和 100% 的缓存率差距影响相近。例如倍率 2、缓存率 1 表示更看重低倍率。缓存率权重为 0 时退回原有低倍率优先。

### 分组倍率保护

每个账号可独立启用分组保护，并选择：

- `gt`：账号倍率 `>` 任一所属分组倍率时命中；
- `gte`：账号倍率 `>=` 任一所属分组倍率时命中。

命中后，账号全局优先级数值会被精确设置为 `guard_priority`（默认建议 `999`）。应将它配置得大于正常排序区间，才能确保账号后移；系统不会自动对错误的小数值做钳制。保护不移除分组、不暂停账号。系统记录保护前优先级；条件解除后，开启倍率排序的账号回到当前排序结果，未开启倍率排序的账号恢复保护前数值。

### 账号级组合

四条自动化路径互相独立：

| 开关 | 改写内容 | 常见用途 |
| --- | --- | --- |
| 健康探测 | 失败时停调、成功时恢复 | 固定优先级账号只做测活 |
| 倍率同步 | 获取源站倍率并写回 `rate_multiplier` | 成本倍率自动维护 |
| 倍率排序 | 按倍率及可选缓存率权重写回 `priority` | 常规文本账号排序 |
| 分组保护 | 命中条件时写入保护优先级 | 防止成本倒挂 |

例如固定优先级为 `1` 且只需要测活时，只开启健康探测；生图等不适用自动规则的账号可以关闭全部开关。即使全部关闭，站点库存同步仍会以只读方式刷新本地账号和分组快照。

### 文件活动日志

活动日志页使用服务端分页，默认每页 50 条，界面可选 25、50、100 或 200 条。新活动事件不写入 PostgreSQL，而是按 UTC 日期追加到 `S2AM_LOG_DIR` 下的 JSONL 文件；API 在读取时按当前用户过滤并返回总数、总页数及前后页状态。探测明细仍是独立的 PostgreSQL 历史数据，默认保留 90 天。旧版本的数据库活动日志会在启动初始化或 `--migrate-only` 时幂等导出，全部成功后才删除旧表。

### 多用户隔离

- 空数据库中的第一个用户通过初始化接口创建，并固定为管理员。
- 之后只有管理员可以创建、停用、改密、调整角色或删除用户；系统不开放公共注册。
- 每个用户可以管理自己的多个 Sub2API 站点。
- 站点、账号、分组、探测记录和活动日志都以站点所有者隔离。管理员的全局权限仅用于用户管理，并不会自动读取其他用户的站点数据。
- 删除用户会级联删除该用户的站点及 PostgreSQL 历史数据；不会删除远端 Sub2API 数据。已经写入 JSONL 的活动日志作为不可变运维记录保留，但已删除用户无法再通过 API 读取。

## 额外适配与运维能力

- 周期同步 Sub2API 账号、分组、账号分组关系、远端状态、倍率和版本信息；远端已删除对象在本地软删除。
- 站点连接测试，区分不可达与 `401/403` 凭据错误。
- 手动库存同步、账号探测、倍率同步和优先级整理。
- PostgreSQL `FOR UPDATE SKIP LOCKED` 与到期租约协调正常任务领取，并允许进程崩溃后由其他实例接管。
- 可配置 Worker 并发，统一执行库存、缓存率采样、排序、探活和倍率任务。
- 审计自动化与人工操作；活动事件按 UTC 日期写入 JSONL，包含站点和账号名称快照。探测明细保留 90 天，过期会话每日清理。活动日志可在控制台设置保留天数，保存后立即清理并每日自动删除更早的记录。
- `/healthz` 进程存活检查与 `/readyz` PostgreSQL 就绪检查。
- JSON 结构化日志输出到 stdout，systemd 部署时由 journald 接收。

## 架构

```text
Browser
  |
  | HTTP/HTTPS
  v
S2AM-GO (single binary, :33777)
  |-- React + Geist UI (embedded static files)
  |-- JSON API / session / CSRF
  |-- scheduler + PostgreSQL leases
  |-- Sub2API and NewAPI clients
  |
  +---- PostgreSQL (users, encrypted credentials, inventory, scheduler state)
  +---- daily audit JSONL files (default ./logs)
  +---- managed Sub2API Admin API
  +---- optional NewAPI rate sources
```

主要目录：

```text
cmd/s2am-go/            process entry point
internal/app/           HTTP API, account logic and scheduler
internal/auditlog/      tenant-isolated daily JSONL activity log
internal/config/        environment configuration
internal/database/      PostgreSQL connection and embedded migrations
internal/secret/        credential encryption
internal/upstream/      Sub2API/NewAPI protocol adapters and SSRF controls
internal/web/           embedded frontend build output
web/                    React/Vite source
deploy/                 systemd unit example
scripts/                release build scripts
```

## 运行要求

- Linux amd64（发布产物目标）
- PostgreSQL 14 或更高版本
- 可访问目标 Sub2API Admin API；使用 NewAPI 倍率源时还需访问相应 NewAPI
- 从源码构建时需要 Go 1.24+、Node.js 20+ 和 npm；Bash 发布脚本还需要 `sha256sum`

运行时不需要 Node.js，前端资源已嵌入二进制。

## 环境变量

应用不会自动读取 `.env` 文件。直接运行时需先将变量导入进程环境；systemd 部署使用 `EnvironmentFile`。

| 变量 | 必填 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `S2AM_DATABASE_URL` | 是 | 无 | PostgreSQL URL，例如 `postgres://s2am:password@127.0.0.1:5432/s2am?sslmode=disable` |
| `S2AM_LOG_DIR` | 否 | `./logs` | 每日活动日志目录；生产环境建议使用 `/var/lib/s2am-go/logs` |
| `S2AM_MASTER_KEY` | 是 | 无 | 恰好 32 字节的 Base64 或十六进制密钥；用于加密站点和 NewAPI 凭据 |
| `S2AM_LISTEN_ADDR` | 否 | `:33777` | Go `net/http` 监听地址；反向代理部署建议 `127.0.0.1:33777` |
| `S2AM_PUBLIC_URL` | 否 | `http://127.0.0.1:33777` | 对外访问根地址；不要带末尾 `/` |
| `S2AM_COOKIE_SECURE` | 否 | `false` | HTTPS 生产环境必须设为 `true` |
| `S2AM_AUTO_MIGRATE` | 否 | `true` | 启动时按顺序执行尚未应用的内嵌迁移 |
| `S2AM_WORKERS` | 否 | `8` | 调度并发数，允许 `1` 到 `128` |
| `S2AM_ALLOW_PRIVATE_UPSTREAMS` | 否 | `false` | 是否允许连接 loopback/RFC1918 等私网源站；见安全章节 |

生成主密钥：

```bash
openssl rand -base64 32
```

主密钥必须长期保持不变。丢失或更换后，数据库中的 Sub2API API Key 和 NewAPI 凭据将无法解密。

### 活动日志文件

活动事件写入 `S2AM_LOG_DIR/audit-YYYY-MM-DD.jsonl`，日期使用事件的 UTC 时间。每行是一个独立 JSON 对象，包含事件 ID、`owner_id`、可选 actor/site/account ID、写入时的站点与账号名称快照、动作、结果、详情和时间。HTTP API 会按当前会话的 owner ID 精确过滤，并且不会返回文件中的 `owner_id` 或 actor ID。

JSONL 与输出到 stdout/journald 的进程诊断日志是两套数据。活动日志可按用户设置的保留天数自动清理已完成日期的文件，不要手工改写当天文件。默认相对路径以进程工作目录为基准。多个进程只有在看到同一个 `S2AM_LOG_DIR` 时才能返回完整的合并活动视图；跨主机部署应使用具备可靠追加写语义的共享存储或将文件集中采集。

示例 systemd unit 只允许写入 `StateDirectory=/var/lib/s2am-go`。如果把 `S2AM_LOG_DIR` 改到其他绝对路径，还必须为服务用户创建目录，并相应调整 unit 的 `ReadWritePaths` 或 `StateDirectory`，否则 `ProtectSystem=strict` 会阻止启动。

二进制参数：

| 参数 | 行为 |
| --- | --- |
| `--version` | 输出版本、commit 和构建时间后退出；不连接数据库 |
| `--migrate-only` | 连接 PostgreSQL、应用未执行迁移后退出；仍需完整环境配置 |

## 本地开发

1. 创建 PostgreSQL 数据库。
2. 根据 `.env.example` 导出环境变量。
3. 构建前端，再运行 Go 服务。

```bash
npm --prefix web ci
npm --prefix web run build
go test ./...
go run ./cmd/s2am-go
```

打开 `http://127.0.0.1:33777`。空数据库会进入初始化页，第一个账户成为管理员。

前端热更新开发可另开终端运行：

```bash
npm --prefix web run dev
```

Vite 默认监听 `5173`，并将 `/api`、`/healthz`、`/readyz` 代理到 `127.0.0.1:33777`。

## 构建 Linux amd64 发布文件

Bash/WSL/Linux：

```bash
chmod +x scripts/build-release.sh
VERSION=vX.Y.Z ./scripts/build-release.sh
```

PowerShell：

```powershell
$env:VERSION = "vX.Y.Z"
./scripts/build-release.ps1
```

脚本会依次执行锁定依赖安装、前端生产构建、`go test ./...`，然后设置 `CGO_ENABLED=0 GOOS=linux GOARCH=amd64`。`dist/` 中只生成：

```text
dist/s2am-go-linux-amd64
dist/s2am-go-linux-amd64.sha256
```

在 Windows 上运行发布脚本前应停止 Vite 开发服务器；`npm ci` 会替换 `node_modules`，正在运行的 `esbuild.exe` 会导致 `EPERM` 文件锁错误。

`Version`、Git commit 和 UTC 构建时间通过 `-ldflags -X` 写入二进制。`VERSION` 与 `COMMIT` 可以显式覆盖自动探测值；设置 `SOURCE_DATE_EPOCH` 可固定构建时间。可用以下命令确认：

```bash
./dist/s2am-go-linux-amd64 --version
(cd dist && sha256sum --check s2am-go-linux-amd64.sha256)
```

Makefile 提供相同入口：

```bash
make test
make build-release
```

## PostgreSQL 原生部署

下面以 Debian/Ubuntu 和本机 PostgreSQL 为例。项目不提供或要求 Docker。

### 1. 安装 PostgreSQL

```bash
sudo apt update
sudo apt install -y postgresql postgresql-client ca-certificates openssl
sudo systemctl enable --now postgresql
```

交互创建登录角色，避免把数据库密码写进 shell 历史：

```bash
sudo -u postgres createuser --pwprompt s2am
sudo -u postgres createdb --owner=s2am --encoding=UTF8 s2am
sudo -u postgres psql -d postgres -c 'REVOKE ALL ON DATABASE s2am FROM PUBLIC;'
```

远程 PostgreSQL 应启用 TLS，并在 URL 中使用 `sslmode=verify-full` 和受信 CA。数据库密码中的 `@`、`:`、`/`、`?`、`#`、`%` 等字符必须按 URI 规则编码。

每个 S2AM-GO 进程的连接池最多使用 20 个 PostgreSQL 连接、至少保留 2 个。部署多个实例时，应把该数量和其他数据库客户端一起计入 `max_connections` 预算。

### 2. 安装二进制和服务用户

```bash
sudo useradd --system --home-dir /opt/s2am-go --shell /usr/sbin/nologin s2am
sudo install -d -o root -g root -m 0755 /opt/s2am-go
sudo install -o root -g root -m 0755 dist/s2am-go-linux-amd64 /opt/s2am-go/s2am-go
sudo install -d -o root -g s2am -m 0750 /etc/s2am-go
sudo install -d -o s2am -g s2am -m 0700 /var/lib/s2am-go/logs
```

### 3. 配置环境文件

创建 `/etc/s2am-go/s2am-go.env`：

```dotenv
S2AM_DATABASE_URL="postgres://s2am:REPLACE_WITH_URI_ENCODED_PASSWORD@127.0.0.1:5432/s2am?sslmode=disable"
S2AM_LOG_DIR="/var/lib/s2am-go/logs"
S2AM_MASTER_KEY="REPLACE_WITH_OPENSSL_OUTPUT"
S2AM_LISTEN_ADDR="127.0.0.1:33777"
S2AM_PUBLIC_URL="https://s2am.example.com"
S2AM_COOKIE_SECURE="true"
S2AM_AUTO_MIGRATE="true"
S2AM_WORKERS="8"
S2AM_ALLOW_PRIVATE_UPSTREAMS="false"
```

```bash
sudo chown root:s2am /etc/s2am-go/s2am-go.env
sudo chmod 0640 /etc/s2am-go/s2am-go.env
```

如果 Sub2API/NewAPI 位于 `127.0.0.1`、内网地址或私有 VPC，必须将 `S2AM_ALLOW_PRIVATE_UPSTREAMS` 设为 `true`。仅在所有 S2AM-GO 用户都受信任时这样做。

### 4. 安装 systemd 单元

```bash
sudo install -o root -g root -m 0644 deploy/s2am-go.service /etc/systemd/system/s2am-go.service
sudo systemctl daemon-reload
sudo systemctl enable --now s2am-go
sudo systemctl status s2am-go
```

查看日志和就绪状态：

```bash
sudo journalctl -u s2am-go -f
curl --fail http://127.0.0.1:33777/healthz
curl --fail http://127.0.0.1:33777/readyz
```

默认 `S2AM_AUTO_MIGRATE=true` 会在 HTTP 服务和 Worker 启动前执行迁移。需要显式迁移窗口时，可改为 `false`，停止服务后通过同一环境文件运行：

```bash
sudo systemctl stop s2am-go
sudo systemd-run --pipe --wait --collect --unit=s2am-go-migrate \
  --property=User=s2am --property=Group=s2am \
  --property=EnvironmentFile=/etc/s2am-go/s2am-go.env \
  /opt/s2am-go/s2am-go --migrate-only
sudo systemctl start s2am-go
```

迁移文件嵌入二进制，每个文件在单独事务中应用，并记录在 `schema_migrations`。升级到文件活动日志时，迁移先将原 `audit_events` 表改名；应用初始化随后在 PostgreSQL advisory lock 下将旧事件写入对应日期的 JSONL，全部成功后才删除旧表。迁移可安全重试，重复 ID 在导出和读取时都会去重。升级前应确认日志目录有足够空间。`--migrate-only` 同样执行这一步，因此运行迁移的用户必须拥有 `S2AM_LOG_DIR` 写权限。

### 5. HTTPS 反向代理

生产环境建议只监听 loopback，由 Nginx/Caddy 提供 TLS。Nginx 核心配置示例：

```nginx
server {
    listen 443 ssl http2;
    server_name s2am.example.com;

    ssl_certificate     /etc/letsencrypt/live/s2am.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/s2am.example.com/privkey.pem;
    client_max_body_size 1m;

    location / {
        proxy_pass http://127.0.0.1:33777;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}
```

同时设置 `S2AM_PUBLIC_URL=https://s2am.example.com` 和 `S2AM_COOKIE_SECURE=true`。不要在公网同时开放原始 `33777` 端口。

## 初始化与日常使用

1. 打开站点，创建第一个管理员。
2. 管理员按需创建普通用户。
3. 当前用户添加自己的 Sub2API 根地址和 Admin API Key，先执行连接测试。
4. 同步库存，确认账号、分组、倍率和当前优先级。
5. 如需跟踪分组成本，在顶部导航的“分组”页绑定来源账号，先保存并手动应用一次规则。
6. 先为少量账号分别启用探活、倍率同步、排序或保护，并运行手动动作验证。
7. 检查活动日志和 Sub2API 实际字段，再逐步扩大范围。

站点参数范围：

| 参数 | 范围 | 默认值 |
| --- | --- | --- |
| 库存同步间隔 | `30` 到 `86400` 秒 | `300` |
| 优先级起始值 | `0` 到 `1000000` | `1` |
| 优先级步长 | `1` 到 `100000` | `1` |
| 排序执行间隔 | `10` 到 `86400` 秒 | `60` |
| 缓存率排序 | 开/关 | 关 |
| 缓存率统计窗口 | `300` 到 `86400` 秒 | `3600` |
| 倍率权重 | `0` 到 `100` | `1` |
| 缓存率权重 | `0` 到 `100` | `1` |

账号参数范围：

| 参数 | 范围 | 默认值 |
| --- | --- | --- |
| 探测间隔 | `10` 到 `86400` 秒 | `30` |
| 探测超时 | `3` 到 `600` 秒 | `7` |
| 连续失败阈值 | `1` 到 `100` | `2` |
| 连续成功恢复阈值 | `1` 到 `100` | `1` |
| 测试模型 | 最多 200 字符；空值使用 Sub2API 默认 | OpenAI 账号为 `gpt-5.5`，其他账号为空 |
| 倍率同步间隔 | `30` 到 `604800` 秒 | `30` |
| 充值换算比 | `> 0` 且 `<= 1000000` | `1` |
| 保护优先级 | `0` 到 `1000000` | `999` |

升级迁移只会调整仍使用旧默认值且对应自动化尚未启用的账号；已启用或已手工修改的配置不会被覆盖。

## HTTP API 与会话行为

API 前缀为 `/api/v1`。成功响应统一为 `{"data": ...}`；错误响应为 `{"error":{"code":"...","message":"..."}}`。请求 JSON 最大读取 1 MiB，未知字段会被拒绝。

活动日志分页响应的 `data` 形如 `{"items":[],"page":1,"page_size":50,"total":0,"total_pages":0,"has_previous":false,"has_next":false}`。旧客户端的 `limit=8` 仍可作为 `page_size=8` 的兼容别名。

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| `GET` | `/healthz` | 进程存活，不检查数据库 |
| `GET` | `/readyz` | 检查 PostgreSQL |
| `GET` | `/api/v1/setup/status` | 是否已有用户 |
| `POST` | `/api/v1/setup` | 原子创建首个管理员，仅可成功一次 |
| `POST` | `/api/v1/auth/login` | 登录并签发会话与 CSRF Cookie |
| `GET` | `/api/v1/auth/me` | 当前用户 |
| `POST` | `/api/v1/auth/logout` | 注销当前会话 |
| `GET` | `/api/v1/version` | 当前构建版本、最新 GitHub Release 与更新状态；成功结果缓存 6 小时 |
| `GET` | `/api/v1/overview` | 当前用户站点、账号和健康状态汇总 |
| `GET/POST` | `/api/v1/sites` | 列出/创建当前用户站点 |
| `PATCH/DELETE` | `/api/v1/sites/{siteID}/` | 更新/删除当前用户站点 |
| `POST` | `/api/v1/sites/{siteID}/test` | 测试连接 |
| `POST` | `/api/v1/sites/{siteID}/sync` | 立即同步库存 |
| `POST` | `/api/v1/sites/{siteID}/reconcile` | 立即执行排序与保护 |
| `GET` | `/api/v1/accounts` | 账号列表（最多 2000 条）；支持 `site_id`、`search`、`state`、`platform`、`group_id` |
| `GET` | `/api/v1/accounts/filter-options` | 当前用户可用筛选项；返回 `platforms` 及带 `id/name/site_id/site_name` 的 `groups` |
| `POST` | `/api/v1/accounts/balances` | 批量读取指定账号的上游余额 |
| `GET/PUT` | `/api/v1/settings/balance-alert` | 读取或更新当前用户的全局余额预警设置 |
| `POST` | `/api/v1/settings/balance-alert/test` | 向已保存的企业微信 webhook 发送测试通知 |
| `PUT` | `/api/v1/accounts/{accountID}/settings` | 完整更新四类账号开关和参数 |
| `PUT` | `/api/v1/accounts/{accountID}/scheduling` | 人工设置账号调度状态并清除 `managed_hold`；请求体为 `{"schedulable":true/false}` |
| `POST` | `/api/v1/accounts/{accountID}/probe` | 立即探测 |
| `POST` | `/api/v1/accounts/{accountID}/rate-sync` | 立即同步倍率 |
| `GET` | `/api/v1/accounts/{accountID}/models` | 读取该 Sub2API 账号可用的探测模型 |
| `GET` | `/api/v1/accounts/{accountID}/source-groups` | 使用已保存凭据读取 NewAPI 分组 |
| `POST` | `/api/v1/accounts/{accountID}/source-groups` | 使用请求中的未保存地址/凭据预览 NewAPI 分组 |
| `GET` | `/api/v1/groups?site_id=&search=` | 当前用户的分组与倍率规则列表 |
| `GET` | `/api/v1/groups/{groupID}/` | 单个分组、规则及绑定详情 |
| `PUT` | `/api/v1/groups/{groupID}/config` | 完整保存 `enabled/mode/offset/expression/bindings/apply` |
| `POST` | `/api/v1/groups/{groupID}/apply` | 立即计算并写回已启用的分组倍率规则 |
| `GET` | `/api/v1/events?page=1&page_size=50` | 当前用户活动日志；`page>=1`，`page_size` 范围 `1..200`，返回 `items/total/total_pages/has_previous/has_next` |
| `GET/POST` | `/api/v1/users` | 管理员列出/创建用户 |
| `PATCH/DELETE` | `/api/v1/users/{userID}` | 管理员更新/删除用户 |

`GET /accounts` 的 `state` 接受 `all/healthy/failing/paused/unknown`；`platform` 做不区分大小写的精确匹配，`group_id` 使用 S2AM-GO 同步后生成的本地分组 UUID。筛选条件可以组合，数据始终限制在当前用户拥有的站点内。

账号响应中的 `source_base_url` 是用户可编辑的 NewAPI 覆盖值，`observed_source_base_url` 是最近库存同步观察到的只读值，二者独立；`source_type_locked` 表示来源类型是否已由人工设置。`source_credential_state` 为 `unknown`、`valid` 或 `invalid`，`source_credential_checked_at` 是最近一次明确认证成功或失败的时间；`GET /accounts/filter-options` 的 `invalid_source_credentials` 返回当前租户的 NewAPI 失效凭据总数。`managed_hold` 只表示当前停调由健康探测托管，不表示人工关闭。Uptime 字段以 `uptime_window_size=60` 为上限，`uptime_total`/`uptime_successes` 是实际窗口计数，`uptime_percent` 在没有样本时为 `null`，`uptime_timeline` 使用按新到旧排列的 `S`/`F` 紧凑状态串。

`PUT /accounts/{accountID}/settings` 是完整设置替换，不是单字段补丁；客户端应提交当前四类开关及全部数值。NewAPI 凭据留空表示保留已保存值，只有 `clear_source_credential=true` 才清除。站点 `PATCH` 同样要求名称、根地址和各间隔等完整可变配置，`api_key` 可以留空以保留旧值。分组 `PUT /config` 也会完整替换绑定列表；`bindings` 是按计算顺序排列的账号 UUID 数组，`apply=true` 仅在规则同时 `enabled=true` 时立即应用。

认证使用两个 Cookie：HttpOnly 的 `s2am_session` 和可由前端读取的 `s2am_csrf`。会话有效期为 30 天。除 `GET`、`HEAD`、`OPTIONS` 外，所有已认证请求必须把 `s2am_csrf` 的原值放入 `X-CSRF-Token` 请求头。

命令行登录示例：

```bash
curl -sS -c cookies.txt -H 'Content-Type: application/json' \
  -d '{"email":"admin@example.com","password":"REPLACE_ME"}' \
  http://127.0.0.1:33777/api/v1/auth/login

csrf="$(awk '$6 == "s2am_csrf" {print $7}' cookies.txt)"
curl -sS -b cookies.txt -H "X-CSRF-Token: $csrf" \
  -X POST http://127.0.0.1:33777/api/v1/sites/SITE_UUID/sync
```

## 安全说明

- Sub2API API Key 和 NewAPI 凭据使用随机 nonce 的 AES-256-GCM 加密；附加认证数据绑定具体站点或账号 ID。
- 新密码先对 UTF-8 原文做 SHA-256，再将 32 字节摘要编码为无填充标准 Base64，最后使用带随机盐的 bcrypt cost 12 存储；这避免了 bcrypt 的 72 字节输入上限。密码长度按 Unicode 字符计数，要求 10 到 128 个字符。开发早期版本直接生成的旧 bcrypt 哈希仍可登录，后续改密会写入新格式。
- 会话 Token 只以 SHA-256 哈希保存，状态修改请求使用独立 CSRF Token 校验。
- 管理员修改用户密码或停用用户时，会在同一事务中撤销该用户的全部现有会话。
- 服务设置 CSP、`X-Frame-Options: DENY`、`nosniff`、严格 referrer/permissions 策略。
- 上游响应限制为 2 MiB，不自动跟随重定向，减少凭据泄漏与内存放大风险。
- 默认拒绝 loopback、RFC1918、CGNAT、link-local、文档网段、unspecified、multicast 和常见 IPv4/IPv6 过渡地址。DNS 解析后会复检并把实际拨号固定到已检查 IP，降低 DNS rebinding 风险；受限模式也不使用环境代理绕过该检查。
- `S2AM_ALLOW_PRIVATE_UPSTREAMS=true` 是实例级放宽：任何能创建站点或配置 NewAPI 来源的用户都可能请求服务可达的私网 HTTP(S) 地址。多用户互联网部署应保持 `false`；确需内网源站时，只给受信用户使用，并用防火墙限制服务出站范围。
- 生产环境必须使用 HTTPS、Secure Cookie、强数据库密码，并将 `/etc/s2am-go/s2am-go.env` 权限限制为 `0640` 或更严。
- 活动 JSONL 包含 owner UUID、操作详情以及站点/账号名称快照，权限应限制为服务用户可读写；不要把 `S2AM_LOG_DIR` 放在 Web 根目录或公共共享目录。
- systemd 示例以无登录用户运行，清空 capabilities，并将本地文件系统设为只读，仅通过 `StateDirectory` 开放 `/var/lib/s2am-go` 写入。不要让 `s2am` 用户拥有二进制或 unit 文件的写权限。
- Admin API Key 具有高权限。建议在 Sub2API 侧使用专用密钥并限制来源 IP；不要复用个人 Cookie。
- 登录同时按“规范化邮箱 + TCP 对端 IP”（15 分钟内 5 次失败）和“TCP 对端 IP”（15 分钟内 30 次失败）两个 HMAC 键限流，命中后封锁 15 分钟；成功登录会清除前者，超过 24 小时的旧记录由定时清理。S2AM-GO 不信任 `X-Forwarded-For` 或 `X-Real-IP`，因此经同一反向代理进入的请求会共享应用层对端 IP 限额；公网部署必须在可信代理上再按真实客户端 IP 限流，并只允许代理访问原始 `33777` 端口。

## 备份与恢复

数据库备份、活动日志目录和主密钥必须成套保存。仅有数据库而没有原 `S2AM_MASTER_KEY` 无法恢复上游凭据；仅恢复数据库也不会恢复活动日志。

```bash
sudo install -d -o postgres -g postgres -m 0700 /var/backups/s2am-go
sudo systemctl stop s2am-go
sudo -u postgres pg_dump --format=custom --dbname=s2am \
  --file=/var/backups/s2am-go/s2am-$(date -u +%Y%m%dT%H%M%SZ).dump
sudo install -o root -g root -m 0600 /etc/s2am-go/s2am-go.env \
  /var/backups/s2am-go/s2am-go.env
sudo tar -C /var/lib/s2am-go -czf \
  /var/backups/s2am-go/audit-$(date -u +%Y%m%dT%H%M%SZ).tar.gz logs
sudo systemctl start s2am-go
```

建议将备份复制到加密的异机存储，并定期做恢复演练。恢复到空数据库的示例：

```bash
sudo systemctl stop s2am-go
sudo -u postgres dropdb --if-exists s2am
sudo -u postgres createdb --owner=s2am --encoding=UTF8 s2am
sudo -u postgres pg_restore --dbname=s2am /secure/path/s2am.dump
sudo install -o root -g s2am -m 0640 /secure/path/s2am-go.env /etc/s2am-go/s2am-go.env
sudo install -d -o s2am -g s2am -m 0700 /var/lib/s2am-go
sudo tar -C /var/lib/s2am-go -xzf /secure/path/audit.tar.gz
sudo chown -R s2am:s2am /var/lib/s2am-go/logs
sudo systemctl start s2am-go
curl --fail http://127.0.0.1:33777/readyz
```

## 升级与回滚

1. 阅读发布说明并备份 PostgreSQL、活动日志目录、环境文件和旧二进制。
2. 校验新文件 SHA-256，运行 `--version`。
3. 停止服务，将新二进制原子替换到 `/opt/s2am-go/s2am-go`。
4. 执行迁移或让 `S2AM_AUTO_MIGRATE=true` 在启动时迁移。
5. 检查 `systemctl status`、`journalctl`、`/readyz`，再验证一个手动探测和倍率同步。

```bash
sha256sum --check s2am-go-linux-amd64.sha256
sudo systemctl stop s2am-go
sudo cp -a /opt/s2am-go/s2am-go /opt/s2am-go/s2am-go.previous
sudo install -o root -g root -m 0755 s2am-go-linux-amd64 /opt/s2am-go/s2am-go.new
sudo mv /opt/s2am-go/s2am-go.new /opt/s2am-go/s2am-go
sudo systemctl start s2am-go
curl --fail http://127.0.0.1:33777/readyz
```

应用迁移是前向迁移，不保证旧二进制兼容新 schema。若升级后必须回滚，应同时恢复升级前数据库备份、活动日志目录、旧二进制和匹配的环境文件，不要只替换二进制。

## 上游兼容性与限制

- 托管站点必须暴露兼容的 Sub2API Admin API，包括账号/分组列表、账号详情与更新、可调度状态、账号测试和倍率探测端点。
- 不同 Sub2API/NewAPI 分支可能改变响应 envelope、SSE 终止事件或分组结构。S2AM-GO 兼容常见直接数组、分页 envelope、NewAPI 映射/数组分组结构；升级上游后仍应先做连接测试和手动动作。
- NewAPI 的 `/api/pricing` 在部分部署中是公开接口；S2AM-GO 在回退前仍要求认证接口成功，避免把无关公开站点误当成用户倍率源。
- 上游跳转不会自动跟随。请填写最终 HTTP(S) 根地址，不要填写会 301/302 跳转的地址。
- 优先级只在单个 Sub2API 实例内有意义；跨实例不存在统一远端优先级。
- 本项目不是 API 请求反向代理，不参与最终用户的模型流量；它只调用管理与探测接口。

## 参考项目

- [langrenjh-alt/S2A-Manager](https://github.com/langrenjh-alt/S2A-Manager)：旧版功能与运维流程参考
- [Wei-Shaw/sub2api](https://github.com/Wei-Shaw/sub2api)：托管站点 Admin API 与调度字段语义
- [QuantumNous/new-api](https://github.com/QuantumNous/new-api)：NewAPI 用户分组倍率接口兼容参考
- [Vercel Geist](https://vercel.com/geist/introduction)：前端视觉与字体体系

## License

[MIT](LICENSE)
