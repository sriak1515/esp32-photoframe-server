package db

import (
	"errors"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/sqlite3"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	mattnsqlite "github.com/mattn/go-sqlite3"
	"gorm.io/gorm"
)

func TestAuthoritativeMigrationPreservesDesiredState(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "upgrade.db")
	gdb, err := Init(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := gdb.DB()
	driver, err := sqlite3.WithInstance(sqlDB, &sqlite3.Config{})
	if err != nil {
		t.Fatal(err)
	}
	m, err := migrate.NewWithDatabaseInstance(migrationsURL(t), "sqlite3", driver)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Migrate(39); err != nil {
		t.Fatal(err)
	}
	const config = `{"device_name":"kept"}`
	const processing = `{"converter":"kept"}`
	const palette = `{"black":{"r":1}}`
	if err := gdb.Exec("INSERT INTO devices(id, device_config, device_processing_settings, device_color_palette, config_last_updated) VALUES (1, ?, ?, ?, 123)", config, processing, palette).Error; err != nil {
		t.Fatal(err)
	}
	if err := m.Up(); err != nil {
		t.Fatal(err)
	}
	var got struct {
		DeviceConfig             string
		DeviceProcessingSettings string
		DeviceColorPalette       string
		ConfigLastUpdated        int64
		ServerAuthoritative      bool
		ConfigSyncPending        bool
	}
	if err := gdb.Table("devices").Where("id = ?", 1).Take(&got).Error; err != nil {
		t.Fatal(err)
	}
	if got.DeviceConfig != config || got.DeviceProcessingSettings != processing || got.DeviceColorPalette != palette || got.ConfigLastUpdated != 123 {
		t.Fatalf("migration changed desired state: %+v", got)
	}
	if !got.ServerAuthoritative || !got.ConfigSyncPending {
		t.Fatalf("migration defaults = authoritative:%v pending:%v, want true/true", got.ServerAuthoritative, got.ConfigSyncPending)
	}
}

// migratedDB opens a fresh SQLite database through the production Init path and
// applies the full migration chain, returning the connection. This is the real
// startup path (Init's DSN enables _foreign_keys=on).
func migratedDB(t *testing.T) *gorm.DB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "migrate.db")
	gdb, err := Init(dbPath)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	sqlDB, err := gdb.DB()
	if err != nil {
		t.Fatalf("db handle: %v", err)
	}
	driver, err := sqlite3.WithInstance(sqlDB, &sqlite3.Config{})
	if err != nil {
		t.Fatalf("sqlite3 driver: %v", err)
	}
	m, err := migrate.NewWithDatabaseInstance(migrationsURL(t), "sqlite3", driver)
	if err != nil {
		t.Fatalf("migrate init: %v", err)
	}
	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		t.Fatalf("migrate up failed: %v", err)
	}
	return gdb
}

// migrationsURL resolves the real db/migrations directory relative to this
// test file, independent of the test's working directory.
func migrationsURL(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not resolve caller path")
	}
	dir := filepath.Join(filepath.Dir(thisFile), "..", "..", "db", "migrations")
	return "file://" + dir
}

// TestMigrationsApplyCleanly applies the full numbered migration chain against a
// fresh SQLite database. This is the production startup path and had no coverage;
// a broken or out-of-order migration would otherwise only surface on a real boot.
func TestMigrationsApplyCleanly(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "migrate.db")
	gdb, err := Init(dbPath)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	sqlDB, err := gdb.DB()
	if err != nil {
		t.Fatalf("db handle: %v", err)
	}
	driver, err := sqlite3.WithInstance(sqlDB, &sqlite3.Config{})
	if err != nil {
		t.Fatalf("sqlite3 driver: %v", err)
	}
	m, err := migrate.NewWithDatabaseInstance(migrationsURL(t), "sqlite3", driver)
	if err != nil {
		t.Fatalf("migrate init: %v", err)
	}
	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		t.Fatalf("migrate up failed: %v", err)
	}

	// Sanity: a table created by a late migration exists...
	var n int
	if err := gdb.Raw("SELECT COUNT(*) FROM albums").Scan(&n).Error; err != nil {
		t.Fatalf("albums table missing after migrate: %v", err)
	}
	// ...and the column dropped by 000027 is gone.
	rows, err := gdb.Raw("PRAGMA table_info(albums)").Rows()
	if err != nil {
		t.Fatalf("pragma table_info: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notnull, pk int
		var name, ctype string
		var dflt interface{}
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("scan column: %v", err)
		}
		if name == "asset_count" {
			t.Fatal("albums.asset_count should have been dropped by migration 000027")
		}
	}
}

func TestPhotoTakenDateMigrationPreservesStateAndRollsBack(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "capture-date.db")
	gdb, err := Init(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := gdb.DB()
	driver, err := sqlite3.WithInstance(sqlDB, &sqlite3.Config{})
	if err != nil {
		t.Fatal(err)
	}
	m, err := migrate.NewWithDatabaseInstance(migrationsURL(t), "sqlite3", driver)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Migrate(40); err != nil {
		t.Fatal(err)
	}
	queries := []string{
		"INSERT INTO devices(id) VALUES (1)",
		"INSERT INTO images(id, source, external_id, photo_taken_at) VALUES (10, 'immich', 'asset', '2024-03-01T23:30:00-02:00')",
		"INSERT INTO albums(id, source, external_id, name) VALUES (20, 'immich', 'album', 'Album')",
		"INSERT INTO image_album_memberships(image_id, album_id) VALUES (10, 20)",
		"INSERT INTO device_image_queue(device_id, image_id, position, source) VALUES (1, 10, 10, 'immich')",
		"INSERT INTO immich_caches(image_id, asset_id, file_path, width, height, cached_at) VALUES (10, 'asset', '/tmp/kept', 1, 1, CURRENT_TIMESTAMP)",
	}
	for _, query := range queries {
		if err := gdb.Exec(query).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := m.Migrate(41); err != nil {
		t.Fatal(err)
	}
	var date string
	if err := gdb.Raw("SELECT photo_taken_date FROM images WHERE id = 10").Scan(&date).Error; err != nil {
		t.Fatal(err)
	}
	if date != "2024-03-01" {
		t.Fatalf("backfill = %q", date)
	}
	for _, table := range []string{"images", "image_album_memberships", "device_image_queue", "immich_caches"} {
		var count int64
		if err := gdb.Table(table).Count(&count).Error; err != nil || count != 1 {
			t.Fatalf("%s count=%d err=%v", table, count, err)
		}
	}
	if err := m.Steps(-1); err != nil {
		t.Fatal(err)
	}
	if gdb.Migrator().HasColumn("images", "photo_taken_date") {
		t.Fatal("rollback retained photo_taken_date")
	}
	var count int64
	if err := gdb.Table("images").Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("rollback images=%d err=%v", count, err)
	}
}

func TestQueueLifecycleMigrationPreservesAndNormalizesLegacyData(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "queue-lifecycle.db")
	gdb, err := Init(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := gdb.DB()
	driver, err := sqlite3.WithInstance(sqlDB, &sqlite3.Config{})
	if err != nil {
		t.Fatal(err)
	}
	m, err := migrate.NewWithDatabaseInstance(migrationsURL(t), "sqlite3", driver)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Migrate(41); err != nil {
		t.Fatal(err)
	}

	exec := func(query string, args ...interface{}) {
		t.Helper()
		if err := gdb.Exec(query, args...).Error; err != nil {
			t.Fatalf("exec %q: %v", query, err)
		}
	}
	exec("INSERT INTO devices(id, name) VALUES (1, 'one'), (2, 'two')")
	exec("INSERT INTO images(id, source, external_id) VALUES (10, 'immich', 'a'), (11, 'gallery', 'b')")

	// Emulate a legacy installation whose position constraint was absent, so the
	// migration's tie-break and repair behavior is exercised rather than assumed.
	exec("ALTER TABLE device_image_queue RENAME TO device_image_queue_constrained")
	exec(`CREATE TABLE device_image_queue (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		device_id INTEGER NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
		image_id INTEGER NOT NULL REFERENCES images(id) ON DELETE CASCADE,
		position INTEGER NOT NULL DEFAULT 0,
		source TEXT NOT NULL DEFAULT '',
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`)
	exec("DROP TABLE device_image_queue_constrained")
	exec("INSERT INTO device_image_queue(id, device_id, image_id, position, source, created_at) VALUES (?, ?, ?, ?, ?, ?)", 103, 1, 10, 5, "immich-snapshot", "2024-04-03 04:05:06")
	exec("INSERT INTO device_image_queue(id, device_id, image_id, position, source, created_at) VALUES (?, ?, ?, ?, ?, ?)", 101, 1, 10, -2, "first-snapshot", "2024-01-02 03:04:05")
	exec("INSERT INTO device_image_queue(id, device_id, image_id, position, source, created_at) VALUES (?, ?, ?, ?, ?, ?)", 102, 1, 11, -2, "second-snapshot", "2024-02-03 04:05:06")
	exec("INSERT INTO device_image_queue(id, device_id, image_id, position, source, created_at) VALUES (?, ?, ?, ?, ?, ?)", 104, 2, 11, 99, "device-two", "2024-05-06 07:08:09")
	exec("INSERT INTO device_histories(id, device_id, image_id, served_at) VALUES (201, 1, 10, '2024-06-07 08:09:10'), (202, 1, 11, '2024-06-08 08:09:10')")

	if err := m.Migrate(42); err != nil {
		t.Fatal(err)
	}

	type queueRow struct {
		ID             uint
		DeviceID       uint
		ImageID        uint
		Position       int
		Source         string
		State          string
		ClaimExpiresAt *string
		LeaseExpiresAt *string
		AttemptCount   int
		NextAttemptAt  *string
		LastAttemptAt  *string
		LastErrorCode  *string
		LastError      *string
		CreatedAt      string
	}
	var rows []queueRow
	if err := gdb.Raw(`SELECT id, device_id, image_id, position, source, state,
		claim_expires_at, lease_expires_at, attempt_count, next_attempt_at,
		last_attempt_at, last_error_code, last_error, created_at
		FROM device_image_queue ORDER BY device_id, position, id`).Scan(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 {
		t.Fatalf("queue rows = %d, want 4", len(rows))
	}
	wantIDs := []uint{101, 102, 103, 104}
	wantPositions := []int{10, 20, 30, 10}
	for i, row := range rows {
		if row.ID != wantIDs[i] || row.Position != wantPositions[i] {
			t.Errorf("row %d identity/order = id:%d position:%d, want id:%d position:%d", i, row.ID, row.Position, wantIDs[i], wantPositions[i])
		}
		if row.State != "pending" || row.AttemptCount != 0 || row.ClaimExpiresAt != nil || row.LeaseExpiresAt != nil || row.NextAttemptAt != nil || row.LastAttemptAt != nil || row.LastErrorCode != nil || row.LastError != nil {
			t.Errorf("row %d lifecycle defaults = %+v", i, row)
		}
	}
	if rows[0].ImageID != 10 || rows[1].ImageID != 11 || rows[2].ImageID != 10 {
		t.Fatalf("duplicate image occurrences or references changed: %+v", rows[:3])
	}
	if rows[0].Source != "first-snapshot" || rows[0].CreatedAt != "2024-01-02T03:04:05Z" {
		t.Fatalf("source/timestamp changed: %+v", rows[0])
	}

	var legacyHistoryCount int
	if err := gdb.Raw("SELECT COUNT(*) FROM device_histories WHERE id IN (201, 202) AND queue_item_id IS NULL").Scan(&legacyHistoryCount).Error; err != nil {
		t.Fatal(err)
	}
	if legacyHistoryCount != 2 {
		t.Fatalf("preserved legacy history rows = %d, want 2", legacyHistoryCount)
	}
	exec("INSERT INTO device_histories(device_id, image_id, queue_item_id, served_at) VALUES (1, 10, 101, '2024-07-01')")
	err = gdb.Exec("INSERT INTO device_histories(device_id, image_id, queue_item_id, served_at) VALUES (1, 10, 101, '2024-07-02')").Error
	var sqliteErr mattnsqlite.Error
	if !errors.As(err, &sqliteErr) || sqliteErr.ExtendedCode != mattnsqlite.ErrConstraintUnique {
		t.Fatalf("duplicate completion key error = %v, want unique constraint", err)
	}

	assertForeignKey := func(table, from, target, onDelete string) {
		t.Helper()
		var count int
		if err := gdb.Raw(`SELECT COUNT(*) FROM pragma_foreign_key_list(?)
			WHERE "from" = ? AND "table" = ? AND on_delete = ?`, table, from, target, onDelete).Scan(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Errorf("%s.%s foreign key to %s ON DELETE %s count = %d", table, from, target, onDelete, count)
		}
	}
	assertForeignKey("device_image_queue", "device_id", "devices", "CASCADE")
	assertForeignKey("device_image_queue", "image_id", "images", "RESTRICT")
	assertForeignKey("device_histories", "device_id", "devices", "CASCADE")
	assertForeignKey("device_histories", "image_id", "images", "SET NULL")
	err = gdb.Exec("INSERT INTO device_image_queue(device_id, image_id, position, source) VALUES (999, 10, 10, 'invalid-device')").Error
	if !errors.As(err, &sqliteErr) || sqliteErr.ExtendedCode != mattnsqlite.ErrConstraintForeignKey {
		t.Fatalf("queue foreign key error = %v, want foreign key constraint", err)
	}
	err = gdb.Exec("DELETE FROM images WHERE id = 10").Error
	if err == nil {
		t.Fatalf("queued image delete error = %v, want restrictive foreign key constraint", err)
	}
	var protectedRows int
	if err := gdb.Raw("SELECT COUNT(*) FROM device_image_queue WHERE image_id = 10").Scan(&protectedRows).Error; err != nil || protectedRows != 2 {
		t.Fatalf("protected queue rows after image delete = %d, err=%v, want 2", protectedRows, err)
	}

	// Every active lifecycle state must roll back as an ordinary legacy row.
	exec("UPDATE device_image_queue SET state = 'claimed', claim_expires_at = '2024-08-01', attempt_count = 1 WHERE id = 101")
	exec("UPDATE device_image_queue SET state = 'leased', lease_expires_at = '2024-08-02' WHERE id = 102")
	exec("UPDATE device_image_queue SET state = 'failed', next_attempt_at = '2024-08-03', last_error_code = 'upstream', last_error = 'safe' WHERE id = 103")
	exec("UPDATE device_image_queue SET state = 'permanently_invalid', last_error_code = 'missing', last_error = 'safe' WHERE id = 104")
	if err := m.Steps(-1); err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{"state", "claim_expires_at", "lease_expires_at", "attempt_count", "next_attempt_at", "last_attempt_at", "last_error_code", "last_error"} {
		if gdb.Migrator().HasColumn("device_image_queue", column) {
			t.Errorf("rollback retained device_image_queue.%s", column)
		}
	}
	if gdb.Migrator().HasColumn("device_histories", "queue_item_id") {
		t.Error("rollback retained device_histories.queue_item_id")
	}
	var rolledBackRows []queueRow
	if err := gdb.Raw("SELECT id, device_id, image_id, position, source, created_at FROM device_image_queue ORDER BY device_id, position, id").Scan(&rolledBackRows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rolledBackRows) != 4 {
		t.Fatalf("rollback queue rows = %d, want 4", len(rolledBackRows))
	}
	for i, row := range rolledBackRows {
		if row.ID != wantIDs[i] || row.Position != wantPositions[i] {
			t.Errorf("rollback row %d = id:%d position:%d, want id:%d position:%d", i, row.ID, row.Position, wantIDs[i], wantPositions[i])
		}
	}
	var historyCount int
	if err := gdb.Raw("SELECT COUNT(*) FROM device_histories").Scan(&historyCount).Error; err != nil {
		t.Fatal(err)
	}
	if historyCount != 3 {
		t.Fatalf("rollback history rows = %d, want 3", historyCount)
	}
	assertForeignKey("device_image_queue", "device_id", "devices", "CASCADE")
	assertForeignKey("device_image_queue", "image_id", "images", "CASCADE")
	assertForeignKey("device_histories", "device_id", "devices", "CASCADE")
	assertForeignKey("device_histories", "image_id", "images", "SET NULL")

	exec("DELETE FROM images WHERE id = 10")
	var queueCount, nulledHistory int
	if err := gdb.Raw("SELECT COUNT(*) FROM device_image_queue WHERE image_id = 10").Scan(&queueCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := gdb.Raw("SELECT COUNT(*) FROM device_histories WHERE image_id IS NULL").Scan(&nulledHistory).Error; err != nil {
		t.Fatal(err)
	}
	if queueCount != 0 || nulledHistory != 2 {
		t.Fatalf("rollback FK actions: queue rows=%d null histories=%d, want 0/2", queueCount, nulledHistory)
	}
}

// TestForeignKeyCascades verifies the ON DELETE CASCADE / SET NULL constraints
// added in migration 000032 actually fire — i.e. that Init enables enforcement
// (_foreign_keys=on) and the rebuilt junction/child tables carry the FKs. This
// is the behavior the removed hand-written cleanup now relies on.
func TestForeignKeyCascades(t *testing.T) {
	gdb := migratedDB(t)

	// Enforcement must be on, or every cascade below is a silent no-op.
	var fk int
	if err := gdb.Raw("PRAGMA foreign_keys").Scan(&fk).Error; err != nil {
		t.Fatalf("read pragma: %v", err)
	}
	if fk != 1 {
		t.Fatalf("foreign_keys pragma = %d, want 1 (enforcement off — cascades won't fire)", fk)
	}

	exec := func(q string) {
		t.Helper()
		if err := gdb.Exec(q).Error; err != nil {
			t.Fatalf("exec %q: %v", q, err)
		}
	}
	count := func(q string) int {
		t.Helper()
		var n int
		if err := gdb.Raw(q).Scan(&n).Error; err != nil {
			t.Fatalf("count %q: %v", q, err)
		}
		return n
	}

	// Seed one parent of each kind plus a child in every constrained table.
	exec("INSERT INTO devices(id) VALUES (1)")
	exec("INSERT INTO images(id, source, external_id) VALUES (10, 'immich', 'x')")
	exec("INSERT INTO albums(id, source, external_id, name) VALUES (100, 'immich', 'e', 'n')")
	exec("INSERT INTO url_sources(id, url) VALUES (500, 'http://u')")
	exec("INSERT INTO image_album_memberships(image_id, album_id) VALUES (10, 100)")
	exec("INSERT INTO device_album_mappings(device_id, album_id) VALUES (1, 100)")
	exec("INSERT INTO device_url_mappings(device_id, url_source_id) VALUES (1, 500)")
	exec("INSERT INTO generative_states(device_id, source) VALUES (1, 'fractal')")
	exec("INSERT INTO device_histories(device_id, image_id, served_at) VALUES (1, 10, '2026-01-01')")

	// Deleting the image cascades its membership and NULLs (not deletes) the
	// history reference.
	exec("DELETE FROM images WHERE id = 10")
	if got := count("SELECT COUNT(*) FROM image_album_memberships"); got != 0 {
		t.Errorf("membership survived image delete: got %d, want 0", got)
	}
	if got := count("SELECT COUNT(*) FROM device_histories WHERE image_id IS NULL"); got != 1 {
		t.Errorf("history image_id not set null: got %d rows null, want 1", got)
	}
	if got := count("SELECT COUNT(*) FROM device_histories"); got != 1 {
		t.Errorf("history row wrongly deleted on image delete: got %d, want 1", got)
	}

	// Deleting the album cascades the device_album_mapping.
	exec("DELETE FROM albums WHERE id = 100")
	if got := count("SELECT COUNT(*) FROM device_album_mappings"); got != 0 {
		t.Errorf("device_album_mapping survived album delete: got %d, want 0", got)
	}

	// Deleting the url source cascades the device_url_mapping.
	exec("DELETE FROM url_sources WHERE id = 500")
	if got := count("SELECT COUNT(*) FROM device_url_mappings"); got != 0 {
		t.Errorf("device_url_mapping survived url_source delete: got %d, want 0", got)
	}

	// Deleting the device cascades its remaining children.
	exec("DELETE FROM devices WHERE id = 1")
	if got := count("SELECT COUNT(*) FROM generative_states"); got != 0 {
		t.Errorf("generative_state survived device delete: got %d, want 0", got)
	}
	if got := count("SELECT COUNT(*) FROM device_histories"); got != 0 {
		t.Errorf("device_history survived device delete: got %d, want 0", got)
	}
}
