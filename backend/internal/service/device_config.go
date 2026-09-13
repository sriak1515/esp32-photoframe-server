package service

import (
	"encoding/json"
	"time"

	"github.com/aitjcize/esp32-photoframe-server/backend/internal/model"
	"gorm.io/gorm"
)

// NextConfigGeneration advances the existing timestamp even when saves occur
// within the same second or the stored generation is ahead of wall-clock time.
func NextConfigGeneration(current int64) int64 {
	next := time.Now().Unix()
	if current > next {
		return current + 1
	}
	return next + 1
}

var serverOnlyProcessingKeys = map[string]struct{}{
	"converter": {}, "autoMode": {}, "epdOptimizePreset": {},
}

// MergeFirmwareProcessing overlays only fields represented and owned by firmware.
func MergeFirmwareProcessing(stored, firmware string) ([]byte, error) {
	merged := map[string]interface{}{}
	_ = json.Unmarshal([]byte(stored), &merged)
	incoming := map[string]interface{}{}
	if err := json.Unmarshal([]byte(firmware), &incoming); err != nil {
		return nil, err
	}
	for key, value := range incoming {
		if _, serverOnly := serverOnlyProcessingKeys[key]; !serverOnly {
			merged[key] = value
		}
	}
	return json.Marshal(merged)
}

// ClearConfigPending clears only the generation delivered by the caller.
func ClearConfigPending(db *gorm.DB, deviceID uint, generation int64) (bool, error) {
	result := db.Model(&model.Device{}).
		Where("id = ? AND config_last_updated = ? AND config_sync_pending = ?", deviceID, generation, true).
		Update("config_sync_pending", false)
	return result.RowsAffected == 1, result.Error
}

// RetainCurrentConfigPending repairs a stale delivery that completed after a
// newer generation, ensuring the current authoritative snapshot is retried.
func RetainCurrentConfigPending(db *gorm.DB, deviceID uint, deliveredGeneration int64) error {
	return db.Model(&model.Device{}).
		Where("id = ? AND server_authoritative = ? AND config_last_updated > ?", deviceID, true, deliveredGeneration).
		Update("config_sync_pending", true).Error
}
