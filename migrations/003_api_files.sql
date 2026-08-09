CREATE TABLE IF NOT EXISTS api_files (
    id VARCHAR(64) PRIMARY KEY,
    tenant_id VARCHAR(128) NOT NULL,
    user_id INTEGER NOT NULL,
    purpose VARCHAR(64) NOT NULL,
    filename VARCHAR(512) NOT NULL,
    bytes BIGINT NOT NULL DEFAULT 0,
    checksum_sha256 CHAR(64) NOT NULL,
    media_type VARCHAR(255) NOT NULL,
    storage_key VARCHAR(1024) NOT NULL UNIQUE,
    channel_id INTEGER NOT NULL DEFAULT 0,
    provider_type INTEGER NOT NULL DEFAULT 0,
    upstream_file_id VARCHAR(255),
    status VARCHAR(32) NOT NULL,
    created_at BIGINT NOT NULL,
    updated_at BIGINT NOT NULL,
    expires_at BIGINT NOT NULL DEFAULT 0,
    deleted_at_timestamp BIGINT NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_api_files_owner ON api_files (tenant_id, user_id, id);
CREATE INDEX IF NOT EXISTS idx_api_files_status ON api_files (status);
