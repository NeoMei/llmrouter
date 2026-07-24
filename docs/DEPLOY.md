# Codex Gateway 部署指南

## 前置要求

- Go 1.22+
- PostgreSQL 15+
- Redis 7+
- Node.js 18+ (仅 CLI 工具需要)

## 1. 环境变量

```bash
# 必需
GATEWAY_AES_KEY=<32字节密钥，hex编码或原始ASCII>
SQL_DSN=postgres://user:pass@localhost:5432/newapi?sslmode=disable
REDIS_CONN_STRING=redis://localhost:6379

# 生成 AES 密钥
openssl rand -hex 32
```

## 2. 数据库

GORM AutoMigrate 在服务启动时自动创建 provider_profiles、provider_models、user_provider_keys、provider_key_pool 四张表并 seed 四家 provider 数据。

如需手动执行 SQL（参考用）：
```bash
psql $SQL_DSN -f migrations/001_provider_tables.sql
psql $SQL_DSN -f migrations/002_seed_providers.sql
```

## 3. 构建

```bash
# 后端（需要 web/dist，先构建前端）
cd web && bun install && bun run build && cd ..
go build -o codex-gateway .

# CLI 工具
cd cli && npm install && npm run build && cd ..
# 全局安装
npm link --prefix cli
```

## 4. 启动

```bash
./codex-gateway
```

验证：
```bash
curl http://localhost:3000/healthz
# {"status":"ok"}
```

## 5. 管理员配置

### 创建 Gateway 渠道

1. 登录 Dashboard → 渠道管理 → 添加渠道
2. 类型选择 **Codex Gateway**
3. 模型列表填入所有 provider_models 的 model_id：
   ```
   deepseek-v4-flash,deepseek-v4-pro,kimi-for-coding,k3,glm-5.2,qwen3-coder-next
   ```
4. base_url 和 key 留空（由 provider_profile 提供）
5. 保存

### 添加平台 Key

```bash
# 查看 provider 列表
curl -H "Authorization: Bearer <admin-token>" \
  http://localhost:3000/api/gateway/providers

# 添加 DeepSeek 平台 key
curl -X POST http://localhost:3000/api/gateway/platform-key \
  -H "Authorization: Bearer <admin-token>" \
  -H "Content-Type: application/json" \
  -d '{"profile_id": 1, "api_key": "sk-xxxx"}'
```

### 从现有渠道迁移

```bash
go run scripts/migrate_channels.go
```

## 6. 用户配置

### BYOK（用户自带 key）

```bash
# 用户查看可用 provider
curl -H "Authorization: Bearer <user-token>" \
  http://localhost:3000/api/gateway/providers

# 添加自己的 DeepSeek key
curl -X POST http://localhost:3000/api/gateway/byok \
  -H "Authorization: Bearer <user-token>" \
  -H "Content-Type: application/json" \
  -d '{"profile_id": 1, "api_key": "sk-user-own-key", "label": "my deepseek"}'
```

### Codex CLI 接入

```bash
# 一键配置
npx codex-gateway init

# 或手动配置 ~/.codex/config.toml
model = "deepseek-v4-flash"
model_provider = "gateway"

[model_providers.gateway]
name = "Codex Gateway"
base_url = "http://your-server:3000/v1"
wire_api = "responses"
env_key = "CODEX_GATEWAY_KEY"

# 设置环境变量
export CODEX_GATEWAY_KEY="sk-your-newapi-token"

# 使用
codex "写一个 hello world"
```

### Claude Code 接入

```bash
export ANTHROPIC_BASE_URL=http://your-server:3000
export ANTHROPIC_API_KEY=sk-your-newapi-token
claude "写一个 hello world"
```

## 7. 多实例部署

```bash
# 每个实例
GATEWAY_AES_KEY=<same-key> \
SQL_DSN=<same-pg> \
REDIS_CONN_STRING=<same-redis> \
./codex-gateway --port 3001

# 负载均衡
nginx / haproxy / k8s Service
```

限流：单实例用 new-api 原生 `ModelRequestRateLimit`；多实例替换为 `RateLimitMiddleware`（在 `router/relay-router.go` 中）。

## 8. 监控

```
GET /healthz   → 存活检查
GET /readyz    → 就绪检查
GET /metrics   → Prometheus 指标
```

关键指标：
- `gateway_requests_total` — 请求计数
- `gateway_request_duration_seconds` — 延迟分布
- `gateway_keypool_active` — 活跃平台 key 数
- `gateway_ratelimited_total` — 限流计数
