CREATE TABLE IF NOT EXISTS fine_tuning_jobs (
    id VARCHAR(64) PRIMARY KEY,
    tenant_id VARCHAR(128) NOT NULL,
    user_id INTEGER NOT NULL,
    training_file_id VARCHAR(64) NOT NULL,
    validation_file_id VARCHAR(64),
    model VARCHAR(255) NOT NULL,
    suffix VARCHAR(64),
    hyperparameters TEXT,
    channel_id INTEGER NOT NULL DEFAULT 0,
    provider_type INTEGER NOT NULL DEFAULT 0,
    upstream_id VARCHAR(255),
    status VARCHAR(32) NOT NULL,
    result_model VARCHAR(255),
    failure_json TEXT,
    billing_snapshot TEXT,
    billing_settled_at BIGINT NOT NULL DEFAULT 0,
    poll_locked_by VARCHAR(128),
    poll_lease_until BIGINT NOT NULL DEFAULT 0,
    poll_after BIGINT NOT NULL DEFAULT 0,
    poll_attempts INTEGER NOT NULL DEFAULT 0,
    version BIGINT NOT NULL DEFAULT 0,
    created_at BIGINT NOT NULL,
    updated_at BIGINT NOT NULL,
    finished_at BIGINT NOT NULL DEFAULT 0,
    cancelled_at BIGINT NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_fine_tuning_owner ON fine_tuning_jobs (tenant_id, user_id, id);
CREATE INDEX IF NOT EXISTS idx_fine_tuning_poll ON fine_tuning_jobs (status, poll_after, poll_lease_until);

CREATE TABLE IF NOT EXISTS fine_tuning_events (
    id BIGINT PRIMARY KEY,
    job_id VARCHAR(64) NOT NULL,
    sequence BIGINT NOT NULL,
    upstream_event_id VARCHAR(255) NOT NULL,
    level VARCHAR(32),
    message TEXT,
    data_json TEXT,
    created_at BIGINT NOT NULL,
    CONSTRAINT idx_fine_tuning_event_sequence UNIQUE (job_id, sequence),
    CONSTRAINT idx_fine_tuning_event_upstream UNIQUE (job_id, upstream_event_id)
);

CREATE INDEX IF NOT EXISTS idx_fine_tuning_events_job ON fine_tuning_events (job_id, sequence);
