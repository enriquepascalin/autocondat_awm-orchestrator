-- Workflow instances (current snapshot)
CREATE TABLE workflow_instances (
    id UUID PRIMARY KEY,
    workflow_definition_id VARCHAR(255) NOT NULL,
    tenant VARCHAR(255) NOT NULL,
    status VARCHAR(50) NOT NULL DEFAULT 'RUNNING',
    current_phase TEXT,
    dimensional_state JSONB NOT NULL DEFAULT '{}',
    version BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    completed_at TIMESTAMP WITH TIME ZONE
);

-- Event history (append-only log)
CREATE TABLE workflow_history (
    id BIGSERIAL PRIMARY KEY,
    workflow_instance_id UUID NOT NULL REFERENCES workflow_instances(id) ON DELETE CASCADE,
    sequence_num BIGINT NOT NULL,
    event_type VARCHAR(100) NOT NULL,
    payload JSONB NOT NULL,
    recorded_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    UNIQUE(workflow_instance_id, sequence_num)
);

-- Pending timers (durable timer storage)
CREATE TABLE timers (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workflow_instance_id UUID NOT NULL REFERENCES workflow_instances(id) ON DELETE CASCADE,
    fire_at TIMESTAMP WITH TIME ZONE NOT NULL,
    timer_type VARCHAR(50) NOT NULL,
    payload JSONB NOT NULL,
    fired BOOLEAN DEFAULT FALSE
);

CREATE INDEX idx_timers_fire_at ON timers(fire_at) WHERE NOT fired;

-- Pending tasks (for task queue)
CREATE TABLE tasks (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workflow_instance_id UUID NOT NULL REFERENCES workflow_instances(id) ON DELETE CASCADE,
    activity_name VARCHAR(255) NOT NULL,
    capabilities VARCHAR(255)[] NOT NULL,
    input JSONB NOT NULL,
    status VARCHAR(50) NOT NULL DEFAULT 'PENDING',
    assigned_agent_id VARCHAR(255),
    deadline TIMESTAMP WITH TIME ZONE,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

CREATE INDEX idx_tasks_status ON tasks(status);
CREATE INDEX idx_tasks_capabilities ON tasks USING GIN(capabilities);