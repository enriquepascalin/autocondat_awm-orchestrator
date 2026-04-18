-- Evidence stored against a completed task (summary, structured data, artifact refs).
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS evidence JSONB;

-- Full audit trail: every status change, claim, progress update, reassignment.
CREATE TABLE IF NOT EXISTS task_audit_log (
    id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    task_id      UUID        NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    event_type   VARCHAR(64) NOT NULL,   -- CREATED, CLAIMED, RELEASED, PROGRESS, COMPLETED, FAILED, REASSIGNED
    agent_id     VARCHAR(255),
    message      TEXT,
    data         JSONB,
    occurred_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_task_audit_task_id   ON task_audit_log(task_id);
CREATE INDEX IF NOT EXISTS idx_task_audit_occurred  ON task_audit_log(task_id, occurred_at);

-- Add version of workflow_definitions table for list/update/delete support.
ALTER TABLE workflow_definitions ADD COLUMN IF NOT EXISTS created_by VARCHAR(255);
