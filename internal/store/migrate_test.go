package store

import (
	"strings"
	"testing"
	"testing/fstest"
)

func TestEmbeddedMigrationsAreContiguous(t *testing.T) {
	ms, err := loadMigrations(migrationFiles, migrationDir)
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	if len(ms) == 0 {
		t.Fatal("no embedded migrations")
	}
	for i, m := range ms {
		if m.version != i+1 {
			t.Errorf("migration %d (%s) has version %d", i, m.name, m.version)
		}
		if strings.TrimSpace(m.sql) == "" {
			t.Errorf("migration %s is empty", m.name)
		}
	}
}

func TestLoadMigrationsRejectsBadSets(t *testing.T) {
	file := func(s string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(s)} }
	cases := map[string]fstest.MapFS{
		"gap": {
			"m/0001_a.sql": file("SELECT 1;"),
			"m/0003_c.sql": file("SELECT 1;"),
		},
		"starts at zero": {
			"m/0000_a.sql": file("SELECT 1;"),
		},
		"duplicate version": {
			"m/0001_a.sql": file("SELECT 1;"),
			"m/0001_b.sql": file("SELECT 1;"),
		},
		"no underscore": {
			"m/0001.sql": file("SELECT 1;"),
		},
		"non-numeric prefix": {
			"m/init_a.sql": file("SELECT 1;"),
		},
		"empty": {
			"m/README.txt": file("not a migration"),
		},
		"missing dir": {},
	}
	for name, fsys := range cases {
		t.Run(name, func(t *testing.T) {
			if ms, err := loadMigrations(fsys, "m"); err == nil {
				t.Fatalf("loadMigrations accepted %v", ms)
			}
		})
	}
}

func TestLoadMigrationsOrdersByVersion(t *testing.T) {
	fsys := fstest.MapFS{
		"m/0002_b.sql": {Data: []byte("B")},
		"m/0001_a.sql": {Data: []byte("A")},
		"m/notes.txt":  {Data: []byte("ignored")},
	}
	ms, err := loadMigrations(fsys, "m")
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	if len(ms) != 2 || ms[0].sql != "A" || ms[1].sql != "B" {
		t.Fatalf("loadMigrations = %+v, want A then B", ms)
	}
}
