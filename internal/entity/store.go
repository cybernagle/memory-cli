package entity

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

type Entity struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Kind        string   `json:"kind"`
	Aliases     []string `json:"aliases"`
	CreatedAt   string   `json:"created_at"`
	UpdatedAt   string   `json:"updated_at"`
	AccessCount int      `json:"access_count"`
	Metadata    string   `json:"metadata"`
}

type EntityStore struct {
	db *sql.DB
}

func NewEntityStore(db *sql.DB) *EntityStore {
	return &EntityStore{db: db}
}

func (s *EntityStore) CreateEntity(ctx context.Context, name, kind string) (*Entity, error) {
	e := &Entity{
		ID:        uuid.New().String(),
		Name:      strings.ToLower(strings.TrimSpace(name)),
		Kind:      kind,
		Aliases:   []string{},
		CreatedAt: time.Now().Format(time.RFC3339),
		UpdatedAt: time.Now().Format(time.RFC3339),
	}
	aliasesJSON, _ := json.Marshal(e.Aliases)
	_, err := s.db.ExecContext(ctx,
		"INSERT INTO entities (id, name, kind, aliases, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)",
		e.ID, e.Name, e.Kind, string(aliasesJSON), e.CreatedAt, e.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("create entity: %w", err)
	}
	return e, nil
}

// UpdateKind sets the kind of an existing entity. Used by the one-off reclassification flow
// (issue #1) to fix the heuristic mislabeling (96% concept): after LLM re-judgment, this
// force-overwrites the kind regardless of how the entity was originally classified. Note that
// Resolve() skips already-existing entities (doesn't re-classify on re-extract), so without
// this explicit UPDATE the mislabeling is permanent.
func (s *EntityStore) UpdateKind(ctx context.Context, id, kind string) error {
	_, err := s.db.ExecContext(ctx,
		"UPDATE entities SET kind = ?, updated_at = ? WHERE id = ?",
		kind, time.Now().Format(time.RFC3339), id)
	if err != nil {
		return fmt.Errorf("update entity kind: %w", err)
	}
	return nil
}

// AllEntities returns every entity row, optionally filtered by kind. Used by the batch
// reclassification command to load the full corpus for LLM re-judgment. Ordered by name so the
// output is deterministic for idempotent re-runs.
func (s *EntityStore) AllEntities(ctx context.Context, kindFilter string) ([]*Entity, error) {
	q := "SELECT id, name, kind, aliases, created_at, updated_at, access_count, metadata FROM entities"
	var args []any
	if kindFilter != "" {
		q += " WHERE kind = ?"
		args = append(args, kindFilter)
	}
	q += " ORDER BY name ASC"
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list entities: %w", err)
	}
	defer rows.Close()
	var out []*Entity
	for rows.Next() {
		var e Entity
		var aliasesJSON string
		if err := rows.Scan(&e.ID, &e.Name, &e.Kind, &aliasesJSON, &e.CreatedAt, &e.UpdatedAt, &e.AccessCount, &e.Metadata); err != nil {
			continue
		}
		json.Unmarshal([]byte(aliasesJSON), &e.Aliases)
		out = append(out, &e)
	}
	return out, nil
}

func (s *EntityStore) FindByName(ctx context.Context, name string) (*Entity, error) {
	row := s.db.QueryRowContext(ctx,
		"SELECT id, name, kind, aliases, created_at, updated_at, access_count, metadata FROM entities WHERE name = ?",
		strings.ToLower(strings.TrimSpace(name)))
	return s.scanEntity(row)
}

func (s *EntityStore) FindByAlias(ctx context.Context, alias string) (*Entity, error) {
	normalized := strings.ToLower(strings.TrimSpace(alias))
	rows, err := s.db.QueryContext(ctx, "SELECT id, name, kind, aliases, created_at, updated_at, access_count, metadata FROM entities")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		e, err := s.scanEntityRow(rows)
		if err != nil {
			continue
		}
		for _, a := range e.Aliases {
			if strings.ToLower(a) == normalized {
				return e, nil
			}
		}
	}
	return nil, nil
}

func (s *EntityStore) FindByID(ctx context.Context, id string) (*Entity, error) {
	row := s.db.QueryRowContext(ctx,
		"SELECT id, name, kind, aliases, created_at, updated_at, access_count, metadata FROM entities WHERE id = ?", id)
	return s.scanEntity(row)
}

func (s *EntityStore) AddAlias(ctx context.Context, entityID, alias string) error {
	e, err := s.FindByID(ctx, entityID)
	if err != nil {
		return err
	}
	if e == nil {
		return fmt.Errorf("entity %s not found", entityID)
	}
	for _, a := range e.Aliases {
		if strings.EqualFold(a, alias) {
			return nil // already exists
		}
	}
	e.Aliases = append(e.Aliases, alias)
	aliasesJSON, _ := json.Marshal(e.Aliases)
	_, err = s.db.ExecContext(ctx, "UPDATE entities SET aliases = ?, updated_at = ? WHERE id = ?",
		string(aliasesJSON), time.Now().Format(time.RFC3339), entityID)
	return err
}

func (s *EntityStore) MergeEntities(ctx context.Context, sourceID, targetID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	// Move all mentions from source to target
	_, err = tx.Exec("UPDATE entity_mentions SET entity_id = ? WHERE entity_id = ?", targetID, sourceID)
	if err != nil {
		tx.Rollback()
		return err
	}
	// Merge source aliases into target
	var sourceAliases, targetAliases string
	tx.QueryRow("SELECT aliases FROM entities WHERE id = ?", sourceID).Scan(&sourceAliases)
	tx.QueryRow("SELECT aliases FROM entities WHERE id = ?", targetID).Scan(&targetAliases)
	var srcList, tgtList []string
	json.Unmarshal([]byte(sourceAliases), &srcList)
	json.Unmarshal([]byte(targetAliases), &tgtList)
	// Add source name as alias of target
	srcName := ""
	tx.QueryRow("SELECT name FROM entities WHERE id = ?", sourceID).Scan(&srcName)
	if srcName != "" {
		tgtList = append(tgtList, srcName)
	}
	tgtList = append(tgtList, srcList...)
	merged, _ := json.Marshal(unique(tgtList))
	_, err = tx.Exec("UPDATE entities SET aliases = ?, updated_at = ? WHERE id = ?",
		string(merged), time.Now().Format(time.RFC3339), targetID)
	if err != nil {
		tx.Rollback()
		return err
	}
	// Delete source entity
	_, err = tx.Exec("DELETE FROM entities WHERE id = ?", sourceID)
	if err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (s *EntityStore) RecordMention(ctx context.Context, entityID, memoryID, mentionText string) error {
	_, err := s.db.ExecContext(ctx,
		"INSERT INTO entity_mentions (entity_id, memory_id, mention_text, created_at) VALUES (?, ?, ?, ?)",
		entityID, memoryID, mentionText, time.Now().Format(time.RFC3339))
	return err
}

func (s *EntityStore) Resolve(ctx context.Context, mention string) (string, bool, error) {
	normalized := strings.ToLower(strings.TrimSpace(mention))

	// Try exact name match
	e, err := s.FindByName(ctx, normalized)
	if err == nil && e != nil {
		return e.ID, true, nil
	}

	// Try alias match
	e, err = s.FindByAlias(ctx, normalized)
	if err == nil && e != nil {
		return e.ID, true, nil
	}

	return "", false, nil
}

func (s *EntityStore) DisplayName(ctx context.Context, id string) (string, error) {
	e, err := s.FindByID(ctx, id)
	if err != nil {
		return "", err
	}
	if e == nil {
		return "", fmt.Errorf("entity %s not found", id)
	}
	return e.Name, nil
}

func (s *EntityStore) scanEntity(row *sql.Row) (*Entity, error) {
	var e Entity
	var aliasesJSON string
	err := row.Scan(&e.ID, &e.Name, &e.Kind, &aliasesJSON, &e.CreatedAt, &e.UpdatedAt, &e.AccessCount, &e.Metadata)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	json.Unmarshal([]byte(aliasesJSON), &e.Aliases)
	if e.Aliases == nil {
		e.Aliases = []string{}
	}
	return &e, nil
}

func (s *EntityStore) scanEntityRow(rows *sql.Rows) (*Entity, error) {
	var e Entity
	var aliasesJSON string
	err := rows.Scan(&e.ID, &e.Name, &e.Kind, &aliasesJSON, &e.CreatedAt, &e.UpdatedAt, &e.AccessCount, &e.Metadata)
	if err != nil {
		return nil, err
	}
	json.Unmarshal([]byte(aliasesJSON), &e.Aliases)
	if e.Aliases == nil {
		e.Aliases = []string{}
	}
	return &e, nil
}

func unique(ss []string) []string {
	seen := make(map[string]bool)
	var result []string
	for _, s := range ss {
		if !seen[s] {
			seen[s] = true
			result = append(result, s)
		}
	}
	return result
}
