package service

import (
	"testing"

	"github.com/aitjcize/esp32-photoframe-server/backend/internal/model"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestUpdateDeviceAuthoritativeModeTransitions(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:device-mode?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Device{}); err != nil {
		t.Fatal(err)
	}
	device := model.Device{Name: "frame", Host: "frame.local", ServerAuthoritative: true, ConfigSyncPending: true}
	if err := db.Create(&device).Error; err != nil {
		t.Fatal(err)
	}
	service := NewDeviceService(DeviceServiceDeps{DB: db})
	disabled := false
	got, err := service.UpdateDevice(device.ID, "frame", "frame.local", "landscape", false, false, false, false, 0, 0, "", "", "", "photo_overlay", "cover", false, "", "", "", "", &disabled)
	if err != nil {
		t.Fatal(err)
	}
	if got.ServerAuthoritative || got.ConfigSyncPending {
		t.Fatalf("disable transition = %+v", got)
	}
	enabled := true
	got, err = service.UpdateDevice(device.ID, "frame", "frame.local", "landscape", false, false, false, false, 0, 0, "", "", "", "photo_overlay", "cover", false, "", "", "", "", &enabled)
	if err != nil {
		t.Fatal(err)
	}
	if !got.ServerAuthoritative || !got.ConfigSyncPending {
		t.Fatalf("enable transition = %+v", got)
	}
}

func TestNewDeviceDefaultsToServerAuthorityWithoutDatabase(t *testing.T) {
	device := model.NewDevice(model.Device{})
	if !device.ServerAuthoritative || !device.ConfigSyncPending {
		t.Fatalf("new device defaults = %+v", device)
	}
}
