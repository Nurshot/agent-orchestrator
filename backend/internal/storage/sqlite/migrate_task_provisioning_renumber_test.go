package sqlite

import (
	"testing"
	"testing/fstest"

	"github.com/pressly/goose/v3"
)

func TestMigrateRepairsRenumberedTaskProvisioningHistory(t *testing.T) {
	db := openMigratedDatabaseCopy(t, 148)
	legacyFS := fstest.MapFS{}
	for legacyPath, canonicalPath := range map[string]string{
		"migrations/0149_session_provisioning.sql": "migrations/0150_session_provisioning.sql",
		"migrations/0150_task_preparations.sql":    "migrations/0151_task_preparations.sql",
	} {
		contents, err := migrationsFS.ReadFile(canonicalPath)
		if err != nil {
			t.Fatalf("read canonical migration %q: %v", canonicalPath, err)
		}
		legacyFS[legacyPath] = &fstest.MapFile{Data: contents}
	}

	gooseMu.Lock()
	goose.SetBaseFS(legacyFS)
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("sqlite3"); err != nil {
		gooseMu.Unlock()
		t.Fatalf("set goose dialect: %v", err)
	}
	if err := goose.Up(db, "migrations"); err != nil {
		gooseMu.Unlock()
		t.Fatalf("apply legacy task-provisioning migrations: %v", err)
	}
	gooseMu.Unlock()

	if err := migrate(db); err != nil {
		t.Fatalf("migrate legacy task-provisioning database: %v", err)
	}
	for table, columns := range map[string][]string{
		"sessions": {"provision_state", "provision_error", "is_task_preparation"},
		"review":   {"interface_mode"},
	} {
		for _, column := range columns {
			var present int
			if err := db.QueryRow(
				`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, table, column,
			).Scan(&present); err != nil {
				t.Fatalf("read %s.%s: %v", table, column, err)
			}
			if present != 1 {
				t.Fatalf("%s.%s count = %d, want 1", table, column, present)
			}
		}
	}
	for _, version := range []int64{149, 150, 151} {
		var applied int
		if err := db.QueryRow(`
SELECT COALESCE((
    SELECT is_applied FROM goose_db_version
    WHERE version_id = ? ORDER BY id DESC LIMIT 1
), 0)`, version).Scan(&applied); err != nil {
			t.Fatalf("read migration %d: %v", version, err)
		}
		if applied != 1 {
			t.Fatalf("migration %d applied = %d, want 1", version, applied)
		}
	}
	if err := migrate(db); err != nil {
		t.Fatalf("second migration pass: %v", err)
	}
}
