-- migrations/001_provider_tables.sql
-- Provider profile subsystem tables for codex-gateway.
-- Note: PostgreSQL-specific (SERIAL, TEXT[], JSONB, BYTEA, TIMESTAMPTZ).
-- The provider layer is a PG-oriented subsystem (see pgxpool dep);
-- cross-database (SQLite/MySQL) parity, if required, is handled in a later task.

CREATE TABLE IF NOT EXISTS provider_profiles (
    id                SERIAL PRIMARY KEY,
    name              VARCHAR(64) UNIQUE NOT NULL,
    display_name      VARCHAR(128),
    base_url          VARCHAR(512) NOT NULL,
    auth_mode         VARCHAR(16) DEFAULT 'api_key',
    adapter_type      VARCHAR(32) DEFAULT 'openai-chat',
    discovery_url     VARCHAR(512) DEFAULT '/v1/models',
    discovery_parser  VARCHAR(32) DEFAULT 'openai',
    status            VARCHAR(16) DEFAULT 'active',
    version           INT DEFAULT 1,
    created_at        TIMESTAMPTZ DEFAULT NOW(),
    updated_at        TIMESTAMPTZ DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS provider_models (
    id                SERIAL PRIMARY KEY,
    profile_id        INT REFERENCES provider_profiles(id) ON DELETE CASCADE,
    model_id          VARCHAR(128) NOT NULL,
    display_name      VARCHAR(128),
    context_window    INT DEFAULT 131072,
    max_output        INT DEFAULT 8192,
    input_modes       TEXT[] DEFAULT '{text}',
    quirk_rules       JSONB DEFAULT '{}',
    status            VARCHAR(16) DEFAULT 'active',
    discovered_at     TIMESTAMPTZ,
    created_at        TIMESTAMPTZ DEFAULT NOW(),
    updated_at        TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE(profile_id, model_id)
);
CREATE INDEX IF NOT EXISTS idx_provider_models_model_id ON provider_models(model_id);

CREATE TABLE IF NOT EXISTS user_provider_keys (
    id                SERIAL PRIMARY KEY,
    user_id           INT NOT NULL,
    profile_id        INT NOT NULL REFERENCES provider_profiles(id),
    encrypted_key     BYTEA NOT NULL,
    key_label         VARCHAR(128),
    status            VARCHAR(16) DEFAULT 'active',
    created_at        TIMESTAMPTZ DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_user_provider_keys_lookup ON user_provider_keys(user_id, profile_id, status);

CREATE TABLE IF NOT EXISTS provider_key_pool (
    id                SERIAL PRIMARY KEY,
    profile_id        INT NOT NULL REFERENCES provider_profiles(id),
    encrypted_key     BYTEA NOT NULL,
    quota_windows     JSONB DEFAULT '{}',
    status            VARCHAR(16) DEFAULT 'active',
    cooldown_until    TIMESTAMPTZ,
    last_error        TEXT,
    last_used_at      TIMESTAMPTZ,
    created_at        TIMESTAMPTZ DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_provider_key_pool_select ON provider_key_pool(profile_id, status);
