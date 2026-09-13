package service

import (
	"testing"

	"github.com/aitjcize/esp32-photoframe-server/backend/internal/model"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestNextConfigGenerationIsStrictlyMonotonic(t *testing.T) {
	future := int64(9_999_999_999)
	if got := NextConfigGeneration(future); got != future+1 {
		t.Fatalf("NextConfigGeneration(%d) = %d, want %d", future, got, future+1)
	}
}

func TestClearConfigPendingRequiresMatchingGeneration(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Device{}); err != nil {
		t.Fatal(err)
	}
	device := model.Device{ConfigLastUpdated: 12, ConfigSyncPending: true, ServerAuthoritative: true}
	if err := db.Create(&device).Error; err != nil {
		t.Fatal(err)
	}
	if cleared, err := ClearConfigPending(db, device.ID, 11); err != nil || cleared {
		t.Fatalf("stale generation cleared=%v err=%v", cleared, err)
	}
	if cleared, err := ClearConfigPending(db, device.ID, 12); err != nil || !cleared {
		t.Fatalf("matching generation cleared=%v err=%v", cleared, err)
	}
}
