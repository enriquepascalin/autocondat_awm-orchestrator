# AWM — Autocondat Workflow Manager

> A durable, event-sourced workflow orchestration engine for human–AI hybrid teams.
> Workflows run for seconds or years. Tasks are claimed by humans, AI agents, or automated services — all through the same gRPC protocol.

---

## Table of Contents

1. [About](#about)
2. [Quick Start](#quick-start)
3. [Architecture](#architecture)
   - [Process Model](#process-model)
   - [State Machine](#state-machine)
   - [Event Sourcing](#event-sourcing)
   - [Task Lifecycle](#task-lifecycle)
4. [Workflow Definition Format](#workflow-definition-format)
   - [Top-Level Fields](#top-level-fields)
   - [State Types](#state-types)
   - [Actions and Tasks](#actions-and-tasks)
   - [Complete Example](#complete-example)
5. [gRPC API Reference](#grpc-api-reference)
   - [Public Service](#public-service)
   - [Orchestrator Service](#orchestrator-service)
6. [Database Schema](#database-schema)
7. [Configuration](#configuration)
8. [Usage Manual](#usage-manual)
   - [Managing Workflow Definitions](#managing-workflow-definitions)
   - [Running Workflows](#running-workflows)
   - [Connecting as an Agent](#connecting-as-an-agent)
   - [Health Check](#health-check)
9. [Development Guide](#development-guide)
   - [Project Layout](#project-layout)
   - [Running Tests](#running-tests)
   - [Adding a New State Type](#adding-a-new-state-type)
10. [Roadmap](#roadmap)

---

## About

AWM is the workflow component of **Autocondat 2026** — a platform for running business processes with hybrid teams of humans, AI agents, and automated services.

### What problem does it solve?

Modern software delivery involves many actors: developers, reviewers, QA engineers, AI agents, deployment scripts, monitoring tools. Coordinating work across these actors today means stitching together Jira, Slack, GitHub Actions, cron jobs, and manual hand-offs — all of which are stateless, ephemeral, and context-unaware.

AWM provides a **single durable runtime** where:

- A workflow is defined once in YAML and can run for **milliseconds to years**
- Each step is executed by whoever the task is assigned to — human CLI, AI agent, or automated service
- Every action, transition, and state change is **permanently recorded** in an append-only event log
- The system **survives restarts**, server failures, and network partitions without losing progress
- Multiple workflow instances run **in isolation** — each gets its own worker subprocess and its own DB-backed queue

### Core Design Principles

| Principle | Implementation |
|---|---|
| **Durability** | Event sourcing — every state change is a DB record before it is acted upon |
| **Isolation** | One `awm-worker` subprocess per active workflow instance |
| **Openness** | Any agent speaks gRPC — CLI tools, LLMs, microservices, cron jobs |
| **Observability** | Full history of every event, task, and transition queryable at any time |
| **Simplicity** | YAML workflow definitions based on CNCF Serverless Workflow spec |

---

## Quick Start

### Prerequisites

- Docker + Docker Compose
- `grpcurl` — `go install github.com/fullstorydev/grpcurl/cmd/grpcurl@latest`
- `jq` — `apt install jq` / `brew install jq`

### 1. Start all services

```bash
cp .env.example .env
docker compose up --build -d
```

This starts PostgreSQL, RabbitMQ, Redis, and the orchestrator. On first boot the container:

1. Compiles both binaries (`awm-orchestrator`, `awm-worker`)
2. Runs all unit tests — aborts if any fail
3. Waits for PostgreSQL to be ready
4. Applies database migrations (`golang-migrate`)
5. Seeds a default tenant
6. Starts the gRPC server on `:9091` and the health server on `:9090`

Wait for the orchestrator:

```bash
until docker logs awm-orchestrator 2>&1 | grep -q "gRPC server listening"; do sleep 2; done && echo "Ready."
```

### 2. Run the smoke test suite

```bash
bash scripts/smoke-test.sh
```

Expected: **13/13 tests passed**, including the full end-to-end path:
`CreateDefinition → StartWorkflow → PollTask → CompleteTask → WORKFLOW_STATUS_COMPLETED`

### 3. Try it manually

```bash
# Create a workflow definition
grpcurl -plaintext -d "$(jq -n \
  --arg tenant "default" \
  --arg name "Hello World" \
  --arg yaml 'id: hello
name: Hello World
version: "1.0"
start: greet
states:
  - name: greet
    type: operation
    end: true
    actions:
      - name: say-hello
        functionRef:
          refName: greet' \
  '{tenant: $tenant, name: $name, yaml_content: $yaml}'
)" localhost:9091 awm.v1.Public/CreateWorkflowDefinition

# Start an instance
START=$(grpcurl -plaintext -d \
  '{"workflow_definition_id":"hello","tenant":"default","input":{"who":"world"}}' \
  localhost:9091 awm.v1.Public/StartWorkflow)
INSTANCE_ID=$(echo "$START" | jq -r '.workflowInstanceId')

# Poll the task as a mock agent
POLL=$(grpcurl -plaintext -d '{"agent_id":"me"}' localhost:9091 awm.v1.Orchestrator/PollTask)
TASK_ID=$(echo "$POLL" | jq -r '.task.taskId')

# Complete the task
grpcurl -plaintext -d "{\"task_id\":\"$TASK_ID\",\"result\":{\"message\":\"Hello, world!\"}}" \
  localhost:9091 awm.v1.Orchestrator/CompleteTask

# Confirm COMPLETED
grpcurl -plaintext -d "{\"workflow_instance_id\":\"$INSTANCE_ID\"}" \
  localhost:9091 awm.v1.Orchestrator/GetWorkflowState | jq '.status'
# "WORKFLOW_STATUS_COMPLETED"
```

---

## Architecture

### Process Model

AWM runs as two cooperating OS processes per active workflow:

```
┌────────────────────────────────────────────────────────────────┐
│  Docker Container / Host                                       │
│                                                                │
│  ┌──────────────────────────────────────┐                      │
│  │  awm-orchestrator  (PID 1)           │  :9091 gRPC          │
│  │                                      │  :9090 HTTP /health  │
│  │  ┌────────────┐  ┌────────────────┐  │                      │
│  │  │ gRPC API   │  │ Timer Service  │  │                      │
│  │  │ Public +   │  │ (1s poll loop) │  │                      │
│  │  │ Orchestr.  │  └────────────────┘  │                      │
│  │  └────────────┘  ┌────────────────┐  │                      │
│  │  ┌────────────┐  │ Definition     │  │                      │
│  │  │ Supervisor │  │ Registry       │  │                      │
│  │  │ (spawns    │  │ (DB + cache +  │  │                      │
│  │  │  workers)  │  │  fs fallback)  │  │                      │
│  │  └─────┬──────┘  └────────────────┘  │                      │
│  └────────┼─────────────────────────────┘                      │
│           │  one subprocess per active workflow instance        │
│           ▼                                                     │
│  ┌──────────────────┐   ┌──────────────────┐                   │
│  │  awm-worker      │   │  awm-worker      │   (N workers)     │
│  │  (instance A)    │   │  (instance B)    │                   │
│  │                  │   │                  │                   │
│  │  • Loads def     │   │  • Loads def     │                   │
│  │    from DB/cache │   │    from DB/cache │                   │
│  │  • Runs state    │   │  • Runs state    │                   │
│  │    machine       │   │    machine       │                   │
│  │  • Consumes task │   │  • Consumes task │                   │
│  │    results from  │   │    results from  │                   │
│  │    RabbitMQ      │   │    RabbitMQ      │                   │
│  └──────────────────┘   └──────────────────┘                   │
│                                                                │
└────────────────────────────────────────────────────────────────┘
         │                    │                  │
         ▼                    ▼                  ▼
    PostgreSQL             RabbitMQ            Redis
    (event log,            (task result        (agent liveness,
     snapshots,             messages)           leases)
     timers, tasks)
```

**Why separate processes?**
Each `awm-worker` is an independent OS process with its own PostgreSQL and RabbitMQ connections. If one workflow's worker crashes, only that instance is affected. The supervisor detects the crash and can restart it up to three times. The orchestrator process itself never executes workflow logic — it stores, queries, and coordinates.

On startup, the orchestrator calls `ListActiveInstances` and spawns one worker per RUNNING instance, so no workflow is lost across restarts.

### State Machine

```mermaid
stateDiagram-v2
    [*] --> RUNNING : StartWorkflow
    RUNNING --> RUNNING : state transition (no tasks)
    RUNNING --> WAITING : delay timer scheduled
    RUNNING --> RUNNING : all tasks in state complete
    WAITING --> RUNNING : timer fires → next state entered
    RUNNING --> COMPLETED : end state reached
    RUNNING --> FAILED : unrecoverable error
    RUNNING --> CANCELED : CancelWorkflow called
    COMPLETED --> [*]
    FAILED --> [*]
    CANCELED --> [*]
```

The `current_phase` column in `workflow_instances` records the name of the YAML state that is currently active. When a state completes (all its tasks done, or immediately for delay/zero-action states), `current_phase` is updated to the next state and the engine calls `executeWorkflow` again.

### Event Sourcing

Every meaningful thing that happens to a workflow instance is written as an immutable event to `workflow_history` **before** the snapshot is updated. This gives you a complete audit trail and enables full replay.

```mermaid
sequenceDiagram
    participant C as Caller (gRPC)
    participant E as Engine
    participant S as Store (PostgreSQL)

    C->>E: StartWorkflow(def, input)
    E->>S: INSERT workflow_instances (status=RUNNING, version=0)
    E->>S: AppendEvents → UPDATE version WHERE version=0<br/>INSERT WorkflowStarted seq=1
    E-->>C: instanceID

    Note over E,S: Worker subprocess takes over

    E->>S: INSERT tasks (status=PENDING)
    E->>S: AppendEvents → UPDATE version WHERE version=1<br/>INSERT TaskScheduled seq=2

    C->>E: CompleteTask(taskID, result)
    E->>S: UPDATE tasks SET status=COMPLETED, output=...
    E->>S: CountPendingTasks → 0
    E->>S: AppendEvents → UPDATE version WHERE version=2<br/>INSERT WorkflowCompleted seq=3
    E->>S: UPDATE workflow_instances SET status=COMPLETED
    E-->>C: {accepted: true}
```

**Optimistic concurrency:** `AppendEvents` runs `UPDATE workflow_instances SET version = version + 1 WHERE id = $1 AND version = $expected RETURNING version`. If a concurrent write changed the version, the `WHERE` clause matches zero rows and `ErrConcurrencyConflict` is returned — no silent data corruption possible.

### Task Lifecycle

```mermaid
stateDiagram-v2
    [*] --> PENDING : engine creates task\n(operation state entered)
    PENDING --> ASSIGNED : agent calls PollTask\nor Connect stream
    ASSIGNED --> COMPLETED : agent calls CompleteTask\n(output stored in DB)
    ASSIGNED --> FAILED : agent calls FailTask
    COMPLETED --> [*] : CountPendingTasks checked\n→ if 0, workflow advances
    FAILED --> [*]
```

When the last PENDING or ASSIGNED task for an instance completes, `CountPendingTasks` returns 0 and the engine calls `advanceFromState`:

```mermaid
flowchart TD
    A["CompleteTask / FailTask"] --> B["store.CompleteTask\n(status=COMPLETED, output stored)"]
    B --> C["CountPendingTasks(instanceID)"]
    C --> D{"pending > 0?"}
    D -- yes --> E["return — still waiting\nfor other tasks in this state"]
    D -- no --> F["LoadInstance\nLoadDefinition"]
    F --> G["findState(currentPhase)"]
    G --> H{"state.End() == true?"}
    H -- yes --> I["AppendEvents WorkflowCompleted\nUpdateWorkflowStatus COMPLETED\n→ worker exits"]
    H -- no --> J["AppendEvents StateTransitioned\nUpdateCurrentPhase(nextState)\nexecuteWorkflow(nextState)"]
    J --> K["Next state entered\n→ new tasks created or\ntimer scheduled"]
```

---

## Workflow Definition Format

Workflow definitions are YAML files based on the [CNCF Serverless Workflow specification](https://serverlessworkflow.io/), extended with AWM-specific fields. They are stored in the database via `CreateWorkflowDefinition` and loaded by the worker via the `DefinitionRegistry` (DB-backed, 5-minute in-memory cache, filesystem fallback).

### Top-Level Fields

```yaml
id: string          # Unique identifier — used as the primary key in workflow_definitions
name: string        # Human-readable display name
version: string     # Semantic version string, e.g. "1.0" or "2.3.1"
start: string       # Name of the first state to execute
states: []          # Ordered list of state definitions (see below)
metadata:           # Optional AWM-specific metadata
  awm:
    tenant: string  # Tenant override (default: taken from StartWorkflow request)
```

### State Types

#### `operation` — Execute one or more tasks

An operation state creates one task per action and suspends until all tasks are complete. When zero actions are defined the state is a no-op and the workflow advances immediately.

```yaml
- name: review-pr
  type: operation
  transition: deploy        # next state when all tasks complete
  # end: true               # use instead of transition for terminal states
  actions:
    - name: run-tests
      capabilities: ["ci-runner"]     # agent must advertise this capability
      roles: ["engineer", "ci-bot"]   # human/AI role names that can perform this
      functionRef:
        refName: runTestSuite
      arguments:                       # passed to agent as task input (JSONB)
        branch: "feature/new-login"
        coverage_threshold: 80
```

| Field | Required | Description |
|---|---|---|
| `name` | ✅ | Unique name within this workflow |
| `type` | ✅ | Must be `"operation"` |
| `transition` | ✅ or `end` | Name of the state to enter after all tasks complete |
| `end` | ✅ or `transition` | Set to `true` for terminal states |
| `actions` | | List of tasks to create. Empty → advance immediately. |
| `actions[].name` | ✅ | Becomes `activity_name` on the task record |
| `actions[].capabilities` | | String array — agent must advertise at least one of these to claim the task |
| `actions[].roles` | | String array — human or AI role names allowed to perform this task |
| `actions[].functionRef.refName` | | Function identifier used by tooling or routing |
| `actions[].arguments` | | Arbitrary key-value map stored as task `input` JSONB |

#### `delay` — Wait for a duration

A delay state schedules a durable timer and suspends execution until it fires. The timer survives orchestrator restarts because it is persisted in PostgreSQL.

```yaml
- name: cool-down
  type: delay
  duration: PT48H           # ISO 8601 duration
  transition: send-reminder  # state to enter when timer fires
```

| Field | Required | Description |
|---|---|---|
| `name` | ✅ | Unique name within this workflow |
| `type` | ✅ | Must be `"delay"` |
| `duration` | ✅ | ISO 8601 (`PT10S`, `PT2H`, `P7D`, `P1DT12H`) or Go format (`10s`, `48h`) |
| `transition` | ✅ | State to enter when the timer fires |

**ISO 8601 duration reference:**

| Expression | Duration |
|---|---|
| `PT30S` | 30 seconds |
| `PT5M` | 5 minutes |
| `PT2H` | 2 hours |
| `P1D` | 1 day (24 hours) |
| `P7D` | 7 days |
| `P1DT12H` | 1 day and 12 hours |
| `P2DT4H30M` | 2 days, 4 hours, 30 minutes |

### Actions and Tasks

Each action in an operation state maps directly to one row in the `tasks` table:

| Task column | Source |
|---|---|
| `activity_name` | `action.name` |
| `state_name` | Name of the containing operation state |
| `capabilities` | `action.capabilities` (empty array if omitted) |
| `roles` | `action.roles` (empty array if omitted) |
| `input` | `action.arguments` (JSONB) |
| `output` | Populated by the agent when calling `CompleteTask` |
| `status` | Starts as `PENDING` |
| `parent_task_id` | `null` for top-level tasks; set programmatically for subtasks |

### Complete Example

A realistic software delivery workflow spanning multiple days:

```yaml
id: user-story-delivery
name: User Story Delivery
version: "1.0"
start: implementation

states:

  - name: implementation
    type: operation
    transition: code-review
    actions:
      - name: implement-feature
        roles: ["developer", "ai-developer"]
        capabilities: ["write-code"]
        functionRef:
          refName: implementUserStory
        arguments:
          story_points: 3
          acceptance_criteria: "User can reset password via email"

  - name: code-review
    type: operation
    transition: qa-hold
    actions:
      - name: review-code
        roles: ["senior-engineer", "tech-lead"]
        capabilities: ["code-review"]
        functionRef:
          refName: reviewPullRequest

  - name: qa-hold
    type: delay
    duration: PT24H
    transition: qa-testing

  - name: qa-testing
    type: operation
    transition: deploy
    actions:
      - name: run-automated-tests
        capabilities: ["ci-runner"]
        functionRef:
          refName: runTestSuite
      - name: exploratory-testing
        roles: ["qa-engineer"]
        functionRef:
          refName: exploratoryTest

  - name: deploy
    type: operation
    end: true
    actions:
      - name: deploy-to-production
        capabilities: ["deploy"]
        roles: ["devops-engineer", "release-manager"]
        functionRef:
          refName: deployToProduction
        arguments:
          environment: production
          require_approval: true
```

---

## gRPC API Reference

The orchestrator exposes two gRPC services on port **9091**. Server reflection is enabled — use `grpcurl -plaintext localhost:9091 list` to introspect all available methods.

### Public Service

`awm.v1.Public` — Manages workflow definitions and triggers workflow execution.

#### `CreateWorkflowDefinition`

Stores a workflow YAML in the database. The YAML is parsed and validated before storage; invalid definitions are rejected.

```bash
grpcurl -plaintext -d "$(jq -n \
  --arg tenant "default" \
  --arg name "My Workflow" \
  --arg yaml "$(cat my-workflow.yaml)" \
  '{tenant: $tenant, name: $name, yaml_content: $yaml}'
)" localhost:9091 awm.v1.Public/CreateWorkflowDefinition
```

**Request fields:**

| Field | Type | Description |
|---|---|---|
| `tenant` | string | Tenant name (e.g. `"default"`) |
| `name` | string | Human-readable display name |
| `yaml_content` | string | Full YAML workflow definition |

**Response:** `WorkflowDefinition` with `id` (from YAML), `version`, `created_at`, `updated_at`.

---

#### `GetWorkflowDefinition`

```bash
grpcurl -plaintext -d '{"id":"user-story-delivery"}' \
  localhost:9091 awm.v1.Public/GetWorkflowDefinition
```

---

#### `StartWorkflow`

Creates a new workflow instance, persists the `WorkflowStarted` event, and spawns an `awm-worker` subprocess to own execution.

```bash
grpcurl -plaintext -d "$(jq -n \
  --arg def "user-story-delivery" \
  --arg tenant "default" \
  '{workflow_definition_id: $def, tenant: $tenant, input: {story_id: "US-42", priority: "high"}}'
)" localhost:9091 awm.v1.Public/StartWorkflow
```

**Request fields:**

| Field | Type | Description |
|---|---|---|
| `workflow_definition_id` | string | The `id:` from the workflow YAML |
| `tenant` | string | Tenant name |
| `input` | Struct | Arbitrary JSON — becomes `dimensional_state` on the instance |
| `idempotency_key` | string | Optional. Prevents duplicate starts for the same logical operation. |
| `scheduled_at` | Timestamp | Optional. Future start time. |

**Response:**

```json
{
  "workflowInstanceId": "550e8400-e29b-41d4-a716-446655440000",
  "orchestratorEndpoint": "localhost:9091"
}
```

---

#### `CancelWorkflow`

Signals the workflow to stop. Sets status to `CANCELED` and terminates the worker subprocess.

```bash
grpcurl -plaintext -d \
  '{"workflow_instance_id":"<uuid>","reason":"user requested cancellation"}' \
  localhost:9091 awm.v1.Public/CancelWorkflow
```

---

### Orchestrator Service

`awm.v1.Orchestrator` — Used by agents (human CLI tools, AI runners, automated services) to receive and complete tasks.

#### `Connect` — Bidirectional streaming (recommended)

Best for long-lived agents. The agent registers once and receives task assignments pushed in real time. The stream stays open for the lifetime of the agent process.

```bash
grpcurl -plaintext -d @ localhost:9091 awm.v1.Orchestrator/Connect <<'EOF'
{"register": {
  "agent_id": "dev-agent-001",
  "tenant": "default",
  "agent_type": "ai_runner",
  "capabilities": ["write-code", "code-review"],
  "max_concurrent_tasks": 3
}}
EOF
```

**Messages sent by the agent (AgentToServer):**

| Variant | When | Key fields |
|---|---|---|
| `register` | Once, on connect | `agent_id`, `tenant`, `capabilities`, `max_concurrent_tasks` |
| `task_result.success` | Task completed | `task_id`, result Struct |
| `task_result.error` | Task failed | `task_id`, error code/message, `retryable` |
| `heartbeat` | Periodically | `agent_id`, `active_task_ids` |
| `disconnect` | Before shutdown | `reason` |

**Messages pushed by the server (ServerToAgent):**

| Variant | Description |
|---|---|
| `ack` | Confirms registration. `success: true` means the agent is registered. |
| `task_assignment` | A task is ready. Contains `task_id`, `workflow_instance_id`, `activity_name`, `input`, `deadline`. |
| `signal` | A signal sent to a specific workflow instance (future use). |

---

#### `PollTask` — Unary fallback

For short-lived agents (cron jobs, scripts) that cannot maintain a persistent stream. Returns the next available PENDING task (any capabilities), or an empty response if nothing is pending.

```bash
grpcurl -plaintext -d '{"agent_id":"my-script","tenant":"default"}' \
  localhost:9091 awm.v1.Orchestrator/PollTask
```

**Polling pattern:**

```bash
while true; do
  TASK=$(grpcurl -plaintext -d '{"agent_id":"poller"}' localhost:9091 awm.v1.Orchestrator/PollTask)
  TASK_ID=$(echo "$TASK" | jq -r '.task.taskId // empty')
  if [ -n "$TASK_ID" ]; then
    echo "Got task $TASK_ID: $(echo "$TASK" | jq -r '.task.activityName')"
    # ... perform work ...
    grpcurl -plaintext -d \
      "$(jq -n --arg id "$TASK_ID" '{task_id: $id, result: {done: true}}')" \
      localhost:9091 awm.v1.Orchestrator/CompleteTask
  fi
  sleep 2
done
```

---

#### `CompleteTask`

Marks a task as completed and stores the agent's output. Triggers the workflow advancement check.

```bash
grpcurl -plaintext -d "$(jq -n \
  --arg id "$TASK_ID" \
  '{task_id: $id, result: {files_changed: 3, tests_passing: true, pr_url: "https://github.com/..."}}'
)" localhost:9091 awm.v1.Orchestrator/CompleteTask
```

The result Struct is stored in the task's `output` JSONB column permanently. After this call, the engine counts remaining PENDING/ASSIGNED tasks for the instance. If zero remain, the workflow advances to the next state or reaches `COMPLETED`.

---

#### `FailTask`

Marks a task as failed. The workflow advancement check still runs — if all tasks are done (even some failed), the workflow may still transition. Error handling logic is state-defined (Phase 4).

```bash
grpcurl -plaintext -d "$(jq -n \
  --arg id "$TASK_ID" \
  '{task_id: $id, error: {code: "ERR_BUILD_FAILED", message: "Tests failed on line 42", retryable: true}}'
)" localhost:9091 awm.v1.Orchestrator/FailTask
```

---

#### `GetWorkflowState`

Query the current snapshot of any workflow instance.

```bash
grpcurl -plaintext -d "{\"workflow_instance_id\":\"$INSTANCE_ID\"}" \
  localhost:9091 awm.v1.Orchestrator/GetWorkflowState
```

**Response:**

```json
{
  "workflowInstanceId": "550e8400-e29b-41d4-a716-446655440000",
  "workflowDefinitionId": "user-story-delivery",
  "status": "WORKFLOW_STATUS_RUNNING",
  "dimensionalState": {
    "story_id": "US-42",
    "priority": "high"
  },
  "createdAt": "2026-04-16T03:53:16Z",
  "updatedAt": "2026-04-16T04:10:54Z"
}
```

**Workflow status values:**

| Value | Meaning |
|---|---|
| `WORKFLOW_STATUS_RUNNING` | Executing or waiting for task completion |
| `WORKFLOW_STATUS_WAITING` | Suspended on a timer |
| `WORKFLOW_STATUS_COMPLETED` | Reached a terminal `end: true` state |
| `WORKFLOW_STATUS_FAILED` | Unrecoverable error |
| `WORKFLOW_STATUS_CANCELED` | Explicitly canceled via `CancelWorkflow` |
| `WORKFLOW_STATUS_SUSPENDED` | Paused (future use) |

---

#### `Heartbeat`

Signals that an agent is alive. Currently returns `{ok: true}`. Redis liveness tracking (Phase 2) will enforce lease expiry for agents that stop heartbeating.

```bash
grpcurl -plaintext -d \
  '{"agent_id":"my-agent","active_task_ids":["task-uuid-1","task-uuid-2"]}' \
  localhost:9091 awm.v1.Orchestrator/Heartbeat
```

---

## Database Schema

```mermaid
erDiagram
    tenants {
        uuid id PK
        varchar name UK
        varchar display_name
        timestamp created_at
    }

    workflow_definitions {
        uuid id PK
        uuid tenant_id FK
        varchar name
        varchar definition_id UK
        int version
        text yaml_content
        timestamp created_at
    }

    workflow_instances {
        uuid id PK
        varchar workflow_definition_id
        varchar tenant
        varchar status
        text current_phase
        jsonb dimensional_state
        bigint version
        timestamp created_at
        timestamp updated_at
        timestamp completed_at
    }

    workflow_history {
        bigserial id PK
        uuid workflow_instance_id FK
        bigint sequence_num
        varchar event_type
        jsonb payload
        timestamp recorded_at
    }

    timers {
        uuid id PK
        uuid workflow_instance_id FK
        timestamp fire_at
        varchar timer_type
        jsonb payload
        boolean fired
    }

    tasks {
        uuid id PK
        uuid workflow_instance_id FK
        varchar state_name
        varchar activity_name
        varchar_array capabilities
        varchar_array roles
        jsonb input
        jsonb output
        varchar status
        varchar assigned_agent_id
        timestamp deadline
        uuid parent_task_id FK
        timestamp created_at
        timestamp updated_at
    }

    tenants ||--o{ workflow_definitions : "owns"
    workflow_instances ||--o{ workflow_history : "append-only log"
    workflow_instances ||--o{ timers : "durable timers"
    workflow_instances ||--o{ tasks : "work items"
    tasks ||--o{ tasks : "subtasks"
```

### Key design decisions

**`workflow_instances.version`**
Monotonically incrementing integer used for optimistic concurrency. `AppendEvents` runs `UPDATE ... WHERE version = $expected`. If two goroutines race to update the same instance, one wins and the other receives `ErrConcurrencyConflict`. No silent data corruption is possible.

**`workflow_history.sequence_num`**
Per-instance event sequence with a `UNIQUE(workflow_instance_id, sequence_num)` constraint. Guarantees event deduplication at the database level.

**`tasks.parent_task_id`**
Self-referencing FK enabling arbitrarily deep task hierarchies. A top-level task (created by an operation state) can spawn sub-tasks. All sub-tasks must complete before the parent is considered done.

**`tasks.output`**
JSONB column populated by `CompleteTask`. The agent's result is permanently stored alongside the task — a full record of what was decided or produced at each step, queryable forever.

**`timers.fired`**
Boolean flag set atomically by `MarkTimerFired`. The timer service polls every second for `WHERE NOT fired AND fire_at <= NOW()`. The flag ensures each timer triggers exactly once even if the service restarts mid-poll.

**`dimensional_state`**
The workflow's running context — the initial `input` passed to `StartWorkflow`, evolving as the workflow progresses. Stored as JSONB; accessible to all states and queryable directly in PostgreSQL.

### History event types

| Event type | When it is written |
|---|---|
| `WorkflowStarted` | Immediately on `StartWorkflow` |
| `TaskScheduled` | When each task is created in an operation state |
| `TimerScheduled` | When a delay state creates a timer |
| `TimerFired` | When the timer service fires a timer |
| `StateTransitioned` | When the workflow advances from one state to the next |
| `WorkflowCompleted` | When a terminal state is reached |

---

## Configuration

Configuration is via environment variables or an `.env` file (copy `.env.example` to start).

| Variable | Default | Description |
|---|---|---|
| `AWM_DB_DSN` | `postgres://awm:awm_dev@localhost:5433/awm_meta?sslmode=disable` | PostgreSQL DSN |
| `AWM_RABBITMQ_URL` | `amqp://awm:awm_dev@localhost:5673/awm` | RabbitMQ connection URL |
| `AWM_REDIS_URL` | `redis://:awm_dev@localhost:6380/0` | Redis connection URL |
| `AWM_WORKER_BINARY` | `./bin/awm-worker` | Path to `awm-worker` binary (inside container: `/awm-worker`) |
| `AWM_GRPC_PORT` | `9091` | gRPC server listen port |
| `POSTGRES_USER` | `awm` | PostgreSQL username |
| `POSTGRES_PASSWORD` | `awm_dev` | PostgreSQL password |
| `POSTGRES_DB` | `awm_meta` | PostgreSQL database name |
| `POSTGRES_PORT` | `5433` | Host-side PostgreSQL port |
| `RABBITMQ_USER` | `awm` | RabbitMQ username |
| `RABBITMQ_PASSWORD` | `awm_dev` | RabbitMQ password |
| `RABBITMQ_VHOST` | `awm` | RabbitMQ virtual host |
| `RABBITMQ_AMQP_PORT` | `5673` | Host-side AMQP port |
| `RABBITMQ_MGMT_PORT` | `15673` | RabbitMQ management UI port |
| `REDIS_PASSWORD` | `awm_dev` | Redis password |
| `REDIS_PORT` | `6380` | Host-side Redis port |
| `ORCHESTRATOR_PORT` | `9091` | Host-side gRPC port |
| `ORCHESTRATOR_HEALTH_PORT` | `9090` | Host-side HTTP health port |

### Exposed ports

| Host port | Container port | Protocol | Use |
|---|---|---|---|
| `9091` | `9091` | gRPC / HTTP2 | All gRPC API calls |
| `9090` | `9090` | HTTP | Health check endpoint |
| `5433` | `5432` | TCP | PostgreSQL direct access |
| `5673` | `5672` | AMQP | RabbitMQ messaging |
| `15673` | `15672` | HTTP | RabbitMQ management UI |
| `6380` | `6379` | TCP | Redis direct access |

---

## Usage Manual

### Managing Workflow Definitions

#### Create a definition from a local YAML file

```bash
grpcurl -plaintext -d "$(jq -n \
  --arg tenant "default" \
  --arg name "User Story Delivery" \
  --arg yaml "$(cat workflows/user-story-delivery.yaml)" \
  '{tenant: $tenant, name: $name, yaml_content: $yaml}'
)" localhost:9091 awm.v1.Public/CreateWorkflowDefinition
```

The `id` field in the YAML becomes the primary key — it must be unique per tenant. Re-submitting the same `id` creates a new version (Phase 2 — currently upserts).

#### Retrieve a definition

```bash
grpcurl -plaintext -d '{"id":"user-story-delivery"}' \
  localhost:9091 awm.v1.Public/GetWorkflowDefinition
```

#### Verify it is in the database

```bash
docker exec -i awm-postgres psql -U awm -d awm_meta -c \
  "SELECT definition_id, name, version, created_at FROM workflow_definitions ORDER BY created_at DESC;"
```

---

### Running Workflows

#### Start an instance

```bash
START=$(grpcurl -plaintext -d "$(jq -n \
  --arg def "user-story-delivery" \
  --arg tenant "default" \
  '{workflow_definition_id: $def, tenant: $tenant, input: {story_id: "US-42", sprint: 14}}'
)" localhost:9091 awm.v1.Public/StartWorkflow)

INSTANCE_ID=$(echo "$START" | jq -r '.workflowInstanceId')
echo "Instance: $INSTANCE_ID"
```

#### Check status

```bash
grpcurl -plaintext -d "{\"workflow_instance_id\":\"$INSTANCE_ID\"}" \
  localhost:9091 awm.v1.Orchestrator/GetWorkflowState | jq '{status, current_phase: .dimensionalState}'
```

#### Inspect the full event history

```bash
docker exec -i awm-postgres psql -U awm -d awm_meta -c "
  SELECT sequence_num, event_type, payload, recorded_at
  FROM workflow_history
  WHERE workflow_instance_id = '$INSTANCE_ID'
  ORDER BY sequence_num;"
```

#### Inspect all tasks for an instance

```bash
docker exec -i awm-postgres psql -U awm -d awm_meta -c "
  SELECT id, state_name, activity_name, capabilities, roles, status, deadline, created_at
  FROM tasks
  WHERE workflow_instance_id = '$INSTANCE_ID'
  ORDER BY created_at;"
```

#### View task output after completion

```bash
docker exec -i awm-postgres psql -U awm -d awm_meta -c "
  SELECT activity_name, status, output
  FROM tasks
  WHERE workflow_instance_id = '$INSTANCE_ID' AND status = 'COMPLETED';"
```

---

### Connecting as an Agent

#### Option A: Bidirectional stream (persistent agents, AI runners)

```bash
grpcurl -plaintext -d @ localhost:9091 awm.v1.Orchestrator/Connect <<'EOF'
{"register": {
  "agent_id": "ai-dev-001",
  "tenant": "default",
  "agent_type": "ai_runner",
  "capabilities": ["write-code", "code-review"],
  "max_concurrent_tasks": 3
}}
EOF
```

The server immediately acknowledges with `{"ack": {"success": true}}` then pushes `task_assignment` messages when matching tasks arrive. The agent processes them and sends `task_result` back.

#### Option B: PollTask loop (scripts, cron jobs)

```bash
# Poll once per 2 seconds
while true; do
  TASK=$(grpcurl -plaintext -d '{"agent_id":"ci-bot"}' \
    localhost:9091 awm.v1.Orchestrator/PollTask)
  TASK_ID=$(echo "$TASK" | jq -r '.task.taskId // empty')

  if [ -n "$TASK_ID" ]; then
    ACTIVITY=$(echo "$TASK" | jq -r '.task.activityName')
    INPUT=$(echo "$TASK" | jq '.task.input')

    echo "Executing [$ACTIVITY] — input: $INPUT"

    # ... perform work ...

    grpcurl -plaintext -d "$(jq -n \
      --arg id "$TASK_ID" \
      '{task_id: $id, result: {exit_code: 0, artifacts: ["coverage.html"]}}'
    )" localhost:9091 awm.v1.Orchestrator/CompleteTask
  fi

  sleep 2
done
```

---

### Health Check

The orchestrator exposes an HTTP endpoint that performs a live database ping:

```bash
curl -s http://localhost:9090/health | jq .
```

**Healthy response (200):**
```json
{ "status": "ok" }
```

**Unhealthy response (503):**
```json
{ "status": "unhealthy", "reason": "dial tcp: connection refused" }
```

Suitable for Docker healthchecks, Kubernetes liveness probes, and upstream monitoring:

```yaml
# Kubernetes liveness probe example
livenessProbe:
  httpGet:
    path: /health
    port: 9090
  initialDelaySeconds: 10
  periodSeconds: 10
```

---

## Development Guide

### Project Layout

```
awm-orchestrator/                          ← you are here
├── cmd/
│   ├── awm-orchestrator/main.go           gRPC server, health endpoint, timer service, supervisor
│   └── awm-worker/main.go                 Per-instance subprocess: state machine + RabbitMQ consumer
├── internal/
│   ├── api/
│   │   ├── orchestrator_server.go         Connect stream, PollTask, CompleteTask, FailTask,
│   │   │                                  Heartbeat, GetWorkflowState
│   │   └── public_server.go               CreateDefinition, GetDefinition, StartWorkflow, CancelWorkflow
│   ├── model/
│   │   └── definition.go                  WorkflowDefinition, State, Action, ParseDuration (ISO 8601)
│   ├── parser/
│   │   └── parser.go                      YAML → WorkflowDefinition (operation + delay states)
│   ├── runtime/
│   │   ├── engine.go                      StartWorkflow, advanceFromState, CompleteTask, FailTask,
│   │   │                                  ClaimPendingTask, ResumeAfterTimer, RecoverIncomplete
│   │   └── definition_registry.go         DB persistence + 5-min sync.Map cache + filesystem fallback
│   ├── store/
│   │   ├── store.go                       Store interface + domain types (WorkflowInstance, Task, Timer)
│   │   └── postgres.go                    Full PostgreSQL implementation, manual JSONB scanning,
│   │                                      optimistic concurrency, advisory locks
│   ├── supervisor/
│   │   └── supervisor.go                  Spawns/monitors awm-worker subprocesses, SIGTERM/SIGKILL
│   ├── timer/
│   │   └── service.go                     1-second polling loop, fires durable timers from DB
│   └── worker/
│       └── worker.go                      RabbitMQ consumer + engine.ResumeWorkflow per instance
├── migrations/
│   ├── 000001_init.up.sql                 workflow_instances, workflow_history, timers, tasks
│   ├── 000001_init.down.sql
│   ├── 000002_task_enrichment.up.sql      state_name, roles, parent_task_id, output
│   └── 000002_task_enrichment.down.sql
├── workflows/
│   └── test-workflow.yaml                 Sample workflow definition
├── Dockerfile
├── go.mod / go.sum
└── README.md                              ← this file

Shared resources (monorepo root):
../../proto/awm/v1/
  orchestrator.proto                       Orchestrator service (agent protocol)
  public.proto                             Public service (definition CRUD, workflow management)
  agent.proto                              Shared message types
../../scripts/
  entrypoint.sh                            Build → test → wait for deps → migrate → seed → run
  smoke-test.sh                            13-step end-to-end validation suite
../../compose.yaml                         postgres, rabbitmq, redis, orchestrator with healthchecks
../../.env / .env.example
```

### Running Tests

Unit tests run automatically inside the Docker container on every start. To run them locally:

```bash
cd services/awm-orchestrator
go test ./... -v
```

Individual packages:

```bash
# PostgreSQL store (sqlmock — no real DB needed)
go test ./internal/store/... -v

# Workflow engine (mock store — no real DB needed)
go test ./internal/runtime/... -v

# YAML parser
go test ./internal/parser/... -v

# gRPC handlers
go test ./internal/api/... -v

# Supervisor
go test ./internal/supervisor/... -v
```

End-to-end smoke tests (requires running Docker stack):

```bash
bash scripts/smoke-test.sh
```

The smoke test covers:
1. Docker service health
2. Default tenant existence
3. `CreateWorkflowDefinition`
4. Definition persisted in DB
5. `StartWorkflow`
6. DB state — instances and history
7. `GetWorkflowState` via gRPC
8. Delay state with PT3S ISO 8601 timer
9. **Milestone 1:** operation state → `PollTask` → `CompleteTask` → `WORKFLOW_STATUS_COMPLETED`
10. Health check endpoint (`GET /health`)

### Adding a New State Type

**Step 1 — Define the struct** in [model/definition.go](internal/model/definition.go):

```go
type SwitchState struct {
    BaseState   `yaml:",inline"`
    Conditions  []SwitchCondition `yaml:"conditions"`
    DefaultFlow string            `yaml:"defaultCondition"`
}

type SwitchCondition struct {
    Condition  string `yaml:"condition"`   // expression evaluated against dimensional_state
    Transition string `yaml:"transition"`
}

// SwitchState satisfies the State interface via BaseState embedding.
// End() returns false — switch states always transition.
```

**Step 2 — Register in the parser** in [parser/parser.go](internal/parser/parser.go):

```go
case "switch":
    var s model.SwitchState
    if err := yaml.Unmarshal(rawBytes, &s); err != nil {
        return nil, fmt.Errorf("parse switch state %q: %w", name, err)
    }
    return &s, nil
```

**Step 3 — Handle in the engine** in [runtime/engine.go](internal/runtime/engine.go):

```go
case *model.SwitchState:
    e.processSwitchState(ctx, instance, events, s, def)
```

**Step 4 — Implement the handler:**

```go
func (e *Engine) processSwitchState(ctx context.Context, instance *store.WorkflowInstance,
    events []store.HistoryEvent, state *model.SwitchState, def *model.WorkflowDefinition) {

    // Evaluate each condition against instance.DimensionalState
    nextStateName := state.DefaultFlow
    for _, cond := range state.Conditions {
        if evaluate(cond.Condition, instance.DimensionalState) {
            nextStateName = cond.Transition
            break
        }
    }

    // Construct a synthetic "completed" state and advance
    // (SwitchState has no tasks — it resolves immediately)
    if err := e.store.UpdateCurrentPhase(ctx, instance.ID, nextStateName); err != nil {
        e.store.UpdateWorkflowStatus(ctx, instance.ID, "FAILED")
        return
    }
    e.executeWorkflow(ctx, instance.ID, def)
}
```

**Step 5 — Add tests** in `parser_test.go` and `engine_test.go`.

---

## Roadmap

### Phase 2 — Agent Protocol + awm-cli *(next)*

The gRPC contract is live and validated. The next milestone is `awm-cli` — a Go binary that lets humans and AI systems interact with the orchestrator from the command line.

| Feature | Description |
|---|---|
| `awm-cli work` | Human task terminal — shows pending tasks, accepts input, submits via gRPC |
| `awm-cli agent` | AI agent runner — polls tasks, delegates to a local LLM or external script |
| `awm-cli dashboard` | Embedded local web server for real-time instance monitoring |
| Task lease extension | `ExtendTaskLease` RPC gives agents more time before a task is reclaimed |
| Redis heartbeat tracking | Agent liveness enforced; timed-out tasks returned to PENDING |

### Phase 3 — AWMS Control Plane

Central management for tenants, definitions, and orchestrator provisioning:

- REST + GraphQL API (`awms` Go service)
- Provisioning engine — generates Docker Compose / Kubernetes manifests per workflow deployment
- Orchestrator self-registration and health polling
- `awms-console` — React/Vite web administration UI
- Visual workflow designer with YAML export

### Phase 4 — Advanced Workflow Features

| Feature | Notes |
|---|---|
| `parallel` state — fork/join concurrent branches | Proto and spec defined |
| `switch` state — condition-based branching | — |
| `subFlow` state — child workflows | — |
| `event` / `signal` states — cross-workflow messaging | `SignalWorkflow` RPC defined; returns Unimplemented |
| Cron scheduling for workflow triggers | — |
| Prometheus metrics endpoint | — |
| OpenTelemetry tracing (gRPC + RabbitMQ) | — |
| REST gateway alongside gRPC | — |
| `agent-dashboard` — React human task inbox | Empty scaffold |

### Phase 5 — Production Hardening

- mTLS for gRPC
- API key management for the REST gateway
- Secrets management (Vault / env files — credentials currently in `.env`)
- Kubernetes operator for automated orchestrator provisioning
- Full documentation and onboarding tutorials
- Chaos testing suite (simulated worker crashes, network partitions, DB failover)

---

*Part of the [Autocondat 2026](../../Vision-2026-Workflows.md) project — an open-source platform for hybrid human–AI software teams.*
