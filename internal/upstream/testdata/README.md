# Sub2API 契约样本

这些是人工编写的合成数据，不含真实账号或密钥。

- `sub2api-c043c247-account.json` 对照测试目标 `c043c24774228ba891ddf90d783aa6dc7d0855b5` 的 `backend/internal/handler/dto/types.go` Account 字段，保留凭据被遮蔽时的存在性标记。只验证本项目使用的字段子集。
- `sub2api-legacy-account.json` 模拟缺失运行约束的旧接口；返回 200 不能据此认证为有效备用。

这些文件用于协议兼容回归，不等于目标实例真实入口已验收。真实入口验收记录在项目实施台账中单独列出。
