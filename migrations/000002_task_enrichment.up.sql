-- Task enrichment: state tracking, role assignments, hierarchical subtasks, output storage

ALTER TABLE tasks
    ADD COLUMN IF NOT EXISTS state_name     VARCHAR(255)  NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS roles          VARCHAR(255)[] NOT NULL DEFAULT '{}',
    ADD COLUMN IF NOT EXISTS output         JSONB,
    ADD COLUMN IF NOT EXISTS parent_task_id UUID REFERENCES tasks(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_tasks_parent ON tasks(parent_task_id) WHERE parent_task_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_tasks_state_name ON tasks(workflow_instance_id, state_name);
