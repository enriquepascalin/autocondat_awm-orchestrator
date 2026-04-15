package runtime

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"

	"github.com/enriquepascalin/awm-orchestrator/internal/model"
	"github.com/enriquepascalin/awm-orchestrator/internal/parser"
)

// DefinitionRegistry manages workflow definitions with database persistence and in‑memory caching.
type DefinitionRegistry struct {
	db    *sqlx.DB
	cache sync.Map // map[string]*cachedDefinition
}

type cachedDefinition struct {
	def       *model.WorkflowDefinition
	expiresAt time.Time
}

// NewDefinitionRegistry creates a new registry with the given database connection.
func NewDefinitionRegistry(db *sqlx.DB) *DefinitionRegistry {
	return &DefinitionRegistry{db: db}
}

// Store persists a workflow definition in the database.
func (r *DefinitionRegistry) Store(ctx context.Context, def *model.WorkflowDefinition) error {
	yamlContent := def.YAML
	if yamlContent == "" {
		return fmt.Errorf("YAML content is required to store definition")
	}

	// Tenant is stored as part of the workflow_definitions table; for now we use a default tenant.
	// In a full implementation, tenant would be passed separately.
	tenantID := "00000000-0000-0000-0000-000000000000" // default tenant

	query := `
		INSERT INTO workflow_definitions (id, tenant_id, name, definition_id, version, yaml_content, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, NOW(), NOW())
		ON CONFLICT (tenant_id, definition_id, version) DO UPDATE SET
			name = EXCLUDED.name,
			yaml_content = EXCLUDED.yaml_content,
			updated_at = NOW()
	`
	_, err := r.db.ExecContext(ctx, query,
		uuid.New(), // generate new ID for this version
		tenantID,
		def.Name,
		def.ID,
		def.Version,
		yamlContent,
	)
	if err != nil {
		return fmt.Errorf("store workflow definition: %w", err)
	}

	// Invalidate cache
	r.cache.Delete(def.ID)
	return nil
}

// Get retrieves a workflow definition by its ID.
// It first checks the cache, then the database, and falls back to the filesystem if not found.
func (r *DefinitionRegistry) Get(ctx context.Context, id string) (*model.WorkflowDefinition, error) {
	// Check cache
	if cached, ok := r.cache.Load(id); ok {
		c := cached.(*cachedDefinition)
		if time.Now().Before(c.expiresAt) {
			return c.def, nil
		}
		r.cache.Delete(id)
	}

	// Query database for latest version
	var row struct {
		YAMLContent string `db:"yaml_content"`
	}
	query := `
		SELECT yaml_content
		FROM workflow_definitions
		WHERE definition_id = $1
		ORDER BY version DESC
		LIMIT 1
	`
	err := r.db.GetContext(ctx, &row, query, id)
	if err == nil {
		def, err := parser.Parse([]byte(row.YAMLContent))
		if err != nil {
			return nil, fmt.Errorf("parse YAML from database: %w", err)
		}
		// Cache for 5 minutes
		r.cache.Store(id, &cachedDefinition{
			def:       def,
			expiresAt: time.Now().Add(5 * time.Minute),
		})
		return def, nil
	}

	if !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("query workflow definition: %w", err)
	}

	// Fallback to file system for development
	return r.getFromFile(id)
}

// getFromFile loads a workflow definition from the local filesystem.
// This is intended for development and bootstrapping.
func (r *DefinitionRegistry) getFromFile(id string) (*model.WorkflowDefinition, error) {
	path := fmt.Sprintf("workflows/%s.yaml", id)
	def, err := parser.ParseFile(path)
	if err != nil {
		return nil, err
	}
	// Optionally store in DB for next time (ignore errors)
	_ = r.Store(context.Background(), def)
	return def, nil
}