DROP TABLE IF EXISTS task_audit_log;
ALTER TABLE tasks DROP COLUMN IF EXISTS evidence;
ALTER TABLE workflow_definitions DROP COLUMN IF EXISTS created_by;
