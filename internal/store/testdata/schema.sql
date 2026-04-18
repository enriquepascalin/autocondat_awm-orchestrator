-- Combined test schema: init-db tables + all three migrations in one pass.
-- Types and column names must exactly match what postgres.go queries.

-- ── Extensions ────────────────────────────────────────────────────────────────
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

-- ── Tenants (from init-db.sql) ────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS tenants (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name         VARCHAR(255) UNIQUE NOT NULL,
    display_at   VARCHAR(255) NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ DEFAULT NOW(),
    updated_at   TIMESTAMPTZ DEFAULT NOW()
);

-- ── Workflow definitions (combined: init-db + migration 000003 column) ─────────
-- created_by is VARCHAR here to match the COALESCE(created_by,'') query in postgres.go.
CREATE TABLE IF NOT EXISTS workflow_definitions (
    id            UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     UUID         NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name          VARCHAR(255) NOT NULL,
    definition_id VARCHAR(255) NOT NULL,
    version       INT          NOT NULL DEFAULT 1,
    yaml_content  TEXT         NOT NULL,
    created_by    VARCHAR(255),
    created_at    TIMESTAMPTZ  DEFAULT NOW(),
    updated_at    TIMESTAMPTZ  DEFAULT NOW(),
    UNIQUE(tenant_id, definition_id, version)
);

-- ── Migration 000001 ──────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS workflow_instances (
    id                       UUID         PRIMARY KEY,
    workflow_definition_id   VARCHAR(255) NOT NULL,
    tenant                   VARCHAR(255) NOT NULL,
    status                   VARCHAR(50)  NOT NULL DEFAULT 'RUNNING',
    current_phase            TEXT,
    dimensional_state        JSONB        NOT NULL DEFAULT '{}',
    version                  BIGINT       NOT NULL DEFAULT 0,
    created_at               TIMESTAMPTZ  DEFAULT NOW(),
    updated_at               TIMESTAMPTZ  DEFAULT NOW(),
    completed_at             TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS workflow_history (
    id                    BIGSERIAL PRIMARY KEY,
    workflow_instance_id  UUID       NOT NULL REFERENCES workflow_instances(id) ON DELETE CASCADE,
    sequence_num          BIGINT     NOT NULL,
    event_type            VARCHAR(100) NOT NULL,
    payload               JSONB      NOT NULL,
    recorded_at           TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE(workflow_instance_id, sequence_num)
);

CREATE TABLE IF NOT EXISTS timers (
    id                    UUID      PRIMARY KEY DEFAULT gen_random_uuid(),
    workflow_instance_id  UUID      NOT NULL REFERENCES workflow_instances(id) ON DELETE CASCADE,
    fire_at               TIMESTAMPTZ NOT NULL,
    timer_type            VARCHAR(50) NOT NULL,
    payload               JSONB     NOT NULL,
    fired                 BOOLEAN   DEFAULT FALSE
);

CREATE INDEX IF NOT EXISTS idx_timers_fire_at ON timers(fire_at) WHERE NOT fired;

CREATE TABLE IF NOT EXISTS tasks (
    id                    UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    workflow_instance_id  UUID         NOT NULL REFERENCES workflow_instances(id) ON DELETE CASCADE,
    activity_name         VARCHAR(255) NOT NULL,
    capabilities          VARCHAR(255)[] NOT NULL DEFAULT '{}',
    input                 JSONB        NOT NULL,
    status                VARCHAR(50)  NOT NULL DEFAULT 'PENDING',
    assigned_agent_id     VARCHAR(255),
    deadline              TIMESTAMPTZ,
    created_at            TIMESTAMPTZ  DEFAULT NOW(),
    updated_at            TIMESTAMPTZ  DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_tasks_status       ON tasks(status);
CREATE INDEX IF NOT EXISTS idx_tasks_capabilities ON tasks USING GIN(capabilities);

-- ── Migration 000002 ──────────────────────────────────────────────────────────
ALTER TABLE tasks
    ADD COLUMN IF NOT EXISTS state_name     VARCHAR(255)   NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS roles          VARCHAR(255)[] NOT NULL DEFAULT '{}',
    ADD COLUMN IF NOT EXISTS output         JSONB,
    ADD COLUMN IF NOT EXISTS parent_task_id UUID REFERENCES tasks(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_tasks_parent     ON tasks(parent_task_id) WHERE parent_task_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_tasks_state_name ON tasks(workflow_instance_id, state_name);

-- ── Migration 000003 ──────────────────────────────────────────────────────────
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS evidence JSONB;

CREATE TABLE IF NOT EXISTS task_audit_log (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    task_id     UUID        NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    event_type  VARCHAR(64) NOT NULL,
    agent_id    VARCHAR(255),
    message     TEXT,
    data        JSONB,
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_task_audit_task_id  ON task_audit_log(task_id);
CREATE INDEX IF NOT EXISTS idx_task_audit_occurred ON task_audit_log(task_id, occurred_at);
