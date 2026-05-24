package entity

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestEntityCreateAndResolve(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	comp := NewEntityComponent()
	if err := comp.Init(context.Background(), db); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()

	e, err := comp.Store().CreateEntity(ctx, "Go", "tool")
	if err != nil {
		t.Fatal(err)
	}
	if e.Name != "go" {
		t.Fatalf("expected go (lowercased), got %s", e.Name)
	}

	id, ok, err := comp.Resolve(ctx, "go")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected to resolve Go")
	}
	if id != e.ID {
		t.Fatalf("expected %s, got %s", e.ID, id)
	}

	name, err := comp.Display(ctx, e.ID)
	if err != nil {
		t.Fatal(err)
	}
	if name != "go" {
		t.Fatalf("expected go, got %s", name)
	}
}

func TestEntityAlias(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	comp := NewEntityComponent()
	comp.Init(context.Background(), db)
	ctx := context.Background()

	e, _ := comp.Store().CreateEntity(ctx, "Go", "tool")
	comp.Store().AddAlias(ctx, e.ID, "Golang")

	id, ok, _ := comp.Resolve(ctx, "golang")
	if !ok {
		t.Fatal("expected to resolve via alias")
	}
	if id != e.ID {
		t.Fatalf("expected %s, got %s", e.ID, id)
	}
}

func TestEntityMerge(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	comp := NewEntityComponent()
	comp.Init(context.Background(), db)
	ctx := context.Background()

	source, _ := comp.Store().CreateEntity(ctx, "fingersaver", "project")
	target, _ := comp.Store().CreateEntity(ctx, "makro", "project")
	comp.Store().AddAlias(ctx, source.ID, "fs")

	comp.Store().MergeEntities(ctx, source.ID, target.ID)

	// "fingersaver" should now resolve to target (via alias)
	id, ok, _ := comp.Resolve(ctx, "fingersaver")
	if !ok {
		t.Fatal("fingersaver should resolve to target via alias after merge")
	}
	if id != target.ID {
		t.Fatalf("expected %s, got %s", target.ID, id)
	}

	id, ok, _ = comp.Resolve(ctx, "fs")
	if !ok {
		t.Fatal("alias should resolve to target after merge")
	}
	if id != target.ID {
		t.Fatalf("expected %s, got %s", target.ID, id)
	}
}

func TestEntityRecordMention(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	comp := NewEntityComponent()
	comp.Init(context.Background(), db)
	ctx := context.Background()

	e, _ := comp.Store().CreateEntity(ctx, "memory-cli", "project")
	err = comp.Store().RecordMention(ctx, e.ID, "mem-001", "memory-cli")
	if err != nil {
		t.Fatal(err)
	}
}

func TestEntityResolveNonExistent(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	comp := NewEntityComponent()
	comp.Init(context.Background(), db)
	ctx := context.Background()

	_, ok, _ := comp.Resolve(ctx, "nonexistent")
	if ok {
		t.Fatal("should not resolve nonexistent entity")
	}
}
