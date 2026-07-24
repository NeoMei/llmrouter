-- migrations/002_seed_providers.sql
-- Seed the 4 codex-gateway provider profiles and their models.
-- PostgreSQL-specific (JSONB); the provider subsystem is PG-oriented
-- (see 001_provider_tables.sql). The same seed is also applied cross-DB
-- at startup by model.SeedProviderProfiles (GORM), so applying this file
-- manually is optional — it exists as the canonical reference/dump.

BEGIN;

-- DeepSeek
INSERT INTO provider_profiles (name, display_name, base_url, auth_mode)
VALUES ('deepseek', 'DeepSeek', 'https://api.deepseek.com', 'api_key')
ON CONFLICT (name) DO NOTHING;

INSERT INTO provider_models (profile_id, model_id, display_name, context_window, quirk_rules)
SELECT p.id, 'deepseek-v4-flash', 'DeepSeek V4 Flash', 131072,
       '{"reasoning_mode": "effort", "preserve_reasoning": true}'::jsonb
FROM provider_profiles p WHERE p.name = 'deepseek'
ON CONFLICT (profile_id, model_id) DO NOTHING;

INSERT INTO provider_models (profile_id, model_id, display_name, context_window, quirk_rules)
SELECT p.id, 'deepseek-v4-pro', 'DeepSeek V4 Pro', 131072,
       '{"reasoning_mode": "effort", "preserve_reasoning": true}'::jsonb
FROM provider_profiles p WHERE p.name = 'deepseek'
ON CONFLICT (profile_id, model_id) DO NOTHING;

-- Kimi
INSERT INTO provider_profiles (name, display_name, base_url, auth_mode)
VALUES ('kimi', 'Kimi Coding Plan', 'https://api.kimi.com/coding/v1', 'api_key')
ON CONFLICT (name) DO NOTHING;

INSERT INTO provider_models (profile_id, model_id, display_name, context_window, quirk_rules)
SELECT p.id, 'kimi-for-coding', 'Kimi for Coding', 262144,
       '{"reasoning_mode": "effort", "preserve_reasoning": true, "strip_params": ["temperature", "top_p", "presence_penalty", "frequency_penalty"], "tool_choice": "auto_only"}'::jsonb
FROM provider_profiles p WHERE p.name = 'kimi'
ON CONFLICT (profile_id, model_id) DO NOTHING;

INSERT INTO provider_models (profile_id, model_id, display_name, context_window, quirk_rules)
SELECT p.id, 'k3', 'Kimi K3', 262144,
       '{"reasoning_mode": "effort", "preserve_reasoning": true, "strip_params": ["temperature", "top_p", "presence_penalty", "frequency_penalty"], "effort_map": {"none": "none", "low": "low", "medium": "high", "high": "high", "xhigh": "max", "max": "max"}}'::jsonb
FROM provider_profiles p WHERE p.name = 'kimi'
ON CONFLICT (profile_id, model_id) DO NOTHING;

-- Zhipu
INSERT INTO provider_profiles (name, display_name, base_url, auth_mode)
VALUES ('zhipu', 'Zhipu Coding Plan', 'https://open.bigmodel.cn/api/coding/paas/v4', 'api_key')
ON CONFLICT (name) DO NOTHING;

INSERT INTO provider_models (profile_id, model_id, display_name, context_window, quirk_rules)
SELECT p.id, 'glm-5.2', 'GLM-5.2', 131072,
       '{"reasoning_mode": "effort", "preserve_reasoning": true, "bracket_strip": true}'::jsonb
FROM provider_profiles p WHERE p.name = 'zhipu'
ON CONFLICT (profile_id, model_id) DO NOTHING;

-- Alibaba
INSERT INTO provider_profiles (name, display_name, base_url, auth_mode)
VALUES ('alibaba', 'Alibaba Coding Plan', 'https://coding.dashscope.aliyuncs.com/v1', 'api_key')
ON CONFLICT (name) DO NOTHING;

INSERT INTO provider_models (profile_id, model_id, display_name, context_window, quirk_rules)
SELECT p.id, 'qwen3-coder-next', 'Qwen3 Coder Next', 131072,
       '{"reasoning_mode": "effort", "preserve_reasoning": true}'::jsonb
FROM provider_profiles p WHERE p.name = 'alibaba'
ON CONFLICT (profile_id, model_id) DO NOTHING;

COMMIT;
