DROP INDEX IF EXISTS idx_tasks_state_name;
DROP INDEX IF EXISTS idx_tasks_parent;

ALTER TABLE tasks
    DROP COLUMN IF EXISTS parent_task_id,
    DROP COLUMN IF EXISTS output,
    DROP COLUMN IF EXISTS roles,
    DROP COLUMN IF EXISTS state_name;
