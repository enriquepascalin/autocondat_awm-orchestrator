//go:build integration

package store_test

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

//go:embed testdata/schema.sql
var schemaSQL string

// integrationDB is set by TestMain and shared across all integration tests.
var integrationDB *sqlx.DB

func TestMain(m *testing.M) {
	ctx := context.Background()

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: "postgres:16-alpine",
			Env: map[string]string{
				"POSTGRES_DB":       "awm_test",
				"POSTGRES_USER":     "awm",
				"POSTGRES_PASSWORD": "awm_test",
			},
			ExposedPorts: []string{"5432/tcp"},
			WaitingFor: wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60 * time.Second),
		},
		Started: true,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to start postgres container: %v\n", err)
		os.Exit(1)
	}
	defer container.Terminate(ctx) //nolint:errcheck

	host, err := container.Host(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "container host: %v\n", err)
		os.Exit(1)
	}
	port, err := container.MappedPort(ctx, "5432/tcp")
	if err != nil {
		fmt.Fprintf(os.Stderr, "container port: %v\n", err)
		os.Exit(1)
	}

	dsn := fmt.Sprintf("postgres://awm:awm_test@%s:%s/awm_test?sslmode=disable", host, port.Port())
	db, err := sqlx.Connect("postgres", dsn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "connect to test postgres: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	if _, err := db.ExecContext(ctx, schemaSQL); err != nil {
		fmt.Fprintf(os.Stderr, "apply schema: %v\n", err)
		os.Exit(1)
	}

	integrationDB = db
	os.Exit(m.Run())
}

// truncateAll resets all mutable tables between tests so each test starts clean.
func truncateAll(t *testing.T) {
	t.Helper()
	_, err := integrationDB.Exec(
		`TRUNCATE task_audit_log, tasks, timers, workflow_history, workflow_instances CASCADE`,
	)
	if err != nil {
		t.Fatalf("truncate: %v", err)
	}
}
