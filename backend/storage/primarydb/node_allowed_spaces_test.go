package primarydb

import (
	"database/sql"
	"path/filepath"
	"slices"
	"testing"
)

func nodeByID(t *testing.T, store *Storage, id int32) *Node {
	t.Helper()
	for _, node := range store.ListNodes() {
		if node != nil && node.ID == id {
			return node
		}
	}
	t.Fatalf("node %d not found", id)
	return nil
}

func TestNormalizeAllowedSpacesKeepsTheOpendeploySpace(t *testing.T) {
	cases := []struct {
		name string
		in   []int32
		want []int32
	}{
		{"nil", nil, []int32{OpendeploySpaceID}},
		{"empty", []int32{}, []int32{OpendeploySpaceID}},
		{"omitted", []int32{5, 3}, []int32{0, 3, 5}},
		{"already present", []int32{3, 0}, []int32{0, 3}},
		{"duplicated", []int32{3, 3, 0, 0}, []int32{0, 3}},
		{"negative dropped", []int32{-1, 4}, []int32{0, 4}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeAllowedSpaces(tc.in); !slices.Equal(got, tc.want) {
				t.Fatalf("normalizeAllowedSpaces(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestNewNodeAllowsEverySpaceThatExists(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "primary.db"))
	extra, err := store.CreateSpace("staging")
	if err != nil {
		t.Fatalf("CreateSpace: %v", err)
	}

	node := store.EnsurePrimaryNode("primary", "primary-id")
	want := []int32{OpendeploySpaceID, DefaultSpaceID, extra.ID}
	if !slices.Equal(node.AllowedSpaces, want) {
		t.Fatalf("AllowedSpaces = %v, want %v", node.AllowedSpaces, want)
	}
}

// A space created after a node was enrolled has to reach that node, or the
// first deployment into it would fail everywhere with nothing to explain why.
func TestCreatingASpaceOpensItOnEveryNode(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "primary.db"))
	a := store.EnsurePrimaryNode("node-a", "node-a-id")
	b := store.EnsurePrimaryNode("node-b", "node-b-id")

	space, err := store.CreateSpace("staging")
	if err != nil {
		t.Fatalf("CreateSpace: %v", err)
	}
	for _, id := range []int32{a.ID, b.ID} {
		if got := nodeByID(t, store, id).AllowedSpaces; !slices.Contains(got, space.ID) {
			t.Fatalf("node %d allowed spaces = %v, want it to contain the new space %d", id, got, space.ID)
		}
	}

	if err := store.DeleteSpace(space.ID); err != nil {
		t.Fatalf("DeleteSpace: %v", err)
	}
	for _, id := range []int32{a.ID, b.ID} {
		if got := nodeByID(t, store, id).AllowedSpaces; slices.Contains(got, space.ID) {
			t.Fatalf("node %d allowed spaces = %v, want the deleted space %d gone", id, got, space.ID)
		}
	}
}

func TestSetNodeAllowedSpacesRestoresTheInvariant(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "primary.db"))
	node := store.EnsurePrimaryNode("primary", "primary-id")

	// Deliberately omits the opendeploy space.
	updated, err := store.SetNodeAllowedSpaces(node.Identifier, []int32{DefaultSpaceID})
	if err != nil {
		t.Fatalf("SetNodeAllowedSpaces: %v", err)
	}
	want := []int32{OpendeploySpaceID, DefaultSpaceID}
	if !slices.Equal(updated.AllowedSpaces, want) {
		t.Fatalf("returned AllowedSpaces = %v, want %v", updated.AllowedSpaces, want)
	}
	if got := nodeByID(t, store, node.ID).AllowedSpaces; !slices.Equal(got, want) {
		t.Fatalf("stored AllowedSpaces = %v, want %v", got, want)
	}
}

// The migration backfills to "every space that exists" so upgrades change
// nothing, and keys that off a sentinel so re-running it on the next restart
// cannot undo a narrowing the operator has since made.
func TestNodeAllowedSpacesMigrationBackfillsWithoutResettingNarrowedNodes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "primary.db")

	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE nodes (
        id INTEGER PRIMARY KEY AUTOINCREMENT, enrollment_id INTEGER,
        enrolled_at INTEGER NOT NULL DEFAULT 0, name TEXT NOT NULL,
        identifier TEXT NOT NULL DEFAULT '', roles TEXT NOT NULL DEFAULT '[]',
        addresses TEXT NOT NULL DEFAULT '[]', wg_public_key TEXT NOT NULL DEFAULT '',
        UNIQUE(name), UNIQUE(identifier), UNIQUE(enrollment_id))`); err != nil {
		t.Fatalf("creating legacy table: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO nodes (name, identifier, roles) VALUES (?,?,'[0]'), (?,?,'[1]')`,
		"legacy-a", "legacy-a-id", "legacy-b", "legacy-b-id"); err != nil {
		t.Fatalf("seeding legacy nodes: %v", err)
	}
	// A space beyond the two seeded ones, so "all spaces" is distinguishable
	// from the column default.
	if _, err := db.Exec(`CREATE TABLE spaces (id INTEGER PRIMARY KEY CHECK (id BETWEEN 0 AND 65535), name TEXT NOT NULL DEFAULT '')`); err != nil {
		t.Fatalf("creating spaces table: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO spaces (id, name) VALUES (0,'opendeploy'),(1,'default'),(7,'staging')`); err != nil {
		t.Fatalf("seeding spaces: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	store := Open(path)
	wantAll := []int32{0, 1, 7}
	for _, node := range store.ListNodes() {
		if !slices.Equal(node.AllowedSpaces, wantAll) {
			t.Fatalf("node %q backfilled to %v, want %v", node.Identifier, node.AllowedSpaces, wantAll)
		}
	}

	// Narrow one node to exactly the opendeploy space — the case a '[0]'
	// default could not have told apart from "never backfilled".
	if _, err := store.SetNodeAllowedSpaces("legacy-a-id", nil); err != nil {
		t.Fatalf("SetNodeAllowedSpaces: %v", err)
	}

	// Restart. The backfill must not run again over the narrowed node.
	store = Open(path)
	for _, node := range store.ListNodes() {
		want := wantAll
		if node.Identifier == "legacy-a-id" {
			want = []int32{OpendeploySpaceID}
		}
		if !slices.Equal(node.AllowedSpaces, want) {
			t.Fatalf("after restart node %q = %v, want %v", node.Identifier, node.AllowedSpaces, want)
		}
	}
}
