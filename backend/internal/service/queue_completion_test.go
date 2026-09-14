package service

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aitjcize/esp32-photoframe-server/backend/internal/model"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestQueueCompletionIsIdempotentByOccurrence(t *testing.T) {
	f := newQueueLifecycleFixture(t)
	item := f.add(t, 0, 10, model.QueueStatePending)
	claim, err := f.service.Poll(f.device.ID)
	require.NoError(t, err)
	leased, err := f.service.LeaseClaim(f.device.ID, item.ID, *claim.Item.ClaimExpiresAt)
	require.NoError(t, err)
	f.now = *leased.LeaseExpiresAt

	require.NoError(t, f.service.CompleteDelivery(f.device.ID, item.ID, *leased.LeaseExpiresAt))
	require.NoError(t, f.service.CompleteDelivery(f.device.ID, item.ID, *leased.LeaseExpiresAt))
	require.ErrorIs(t, f.db.First(&model.DeviceQueueItem{}, item.ID).Error, gorm.ErrRecordNotFound)
	var count int64
	require.NoError(t, f.db.Model(&model.DeviceHistory{}).Where("queue_item_id = ?", item.ID).Count(&count).Error)
	require.EqualValues(t, 1, count)
}

func TestQueueCompletionCleanupFailureRollsBackAndCanRetry(t *testing.T) {
	db := dateIntegrationDB(t)
	settings := NewSettingsService(db)
	require.NoError(t, settings.SetImmichDatePair("2024-02-01", ""))
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	queue := NewQueueServiceWithOptions(db, QueueServiceOptions{Now: func() time.Time { return now }})
	queue.SetImmichService(NewImmichService(db, settings), nil)
	device := model.Device{Name: "frame"}
	require.NoError(t, db.Create(&device).Error)
	date := "2024-01-01"
	queuedImage := model.Image{Source: model.SourceImmich, ExternalID: "asset", PhotoTakenDate: &date}
	require.NoError(t, db.Create(&queuedImage).Error)
	expired := now.Add(-time.Second)
	item := model.DeviceQueueItem{
		DeviceID: device.ID, ImageID: queuedImage.ID, Position: 10, Source: model.SourceImmich,
		State: model.QueueStateLeased, LeaseExpiresAt: &expired,
	}
	require.NoError(t, db.Create(&item).Error)
	badPath := filepath.Join(t.TempDir(), "cache-directory")
	require.NoError(t, os.Mkdir(badPath, 0o755))
	cache := model.ImmichCache{ImageID: queuedImage.ID, AssetID: "asset", FilePath: badPath}
	require.NoError(t, db.Create(&cache).Error)

	require.Error(t, queue.CompleteDelivery(device.ID, item.ID, expired))
	require.Equal(t, model.QueueStateLeased, loadQueueLifecycleItem(t, db, item.ID).State)
	var histories int64
	require.NoError(t, db.Model(&model.DeviceHistory{}).Where("queue_item_id = ?", item.ID).Count(&histories).Error)
	require.Zero(t, histories)

	cachePath := filepath.Join(t.TempDir(), "cache.jpg")
	require.NoError(t, os.WriteFile(cachePath, []byte("cached"), 0o600))
	require.NoError(t, db.Model(&cache).Update("file_path", cachePath).Error)
	require.NoError(t, queue.CompleteDelivery(device.ID, item.ID, expired))
	require.NoError(t, queue.CompleteDelivery(device.ID, item.ID, expired))
	require.ErrorIs(t, db.First(&model.DeviceQueueItem{}, item.ID).Error, gorm.ErrRecordNotFound)
	require.ErrorIs(t, db.First(&model.Image{}, queuedImage.ID).Error, gorm.ErrRecordNotFound)
	_, err := os.Stat(cachePath)
	require.True(t, os.IsNotExist(err))
	require.NoError(t, db.Model(&model.DeviceHistory{}).Where("queue_item_id = ?", item.ID).Count(&histories).Error)
	require.EqualValues(t, 1, histories)
}

func TestMalformedImmichPolicyIsScopedToImmichQueueOperations(t *testing.T) {
	db := dateIntegrationDB(t)
	settings := NewSettingsService(db)
	require.NoError(t, settings.Set("immich_date_from", "malformed"))
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	queue := NewQueueServiceWithOptions(db, QueueServiceOptions{Now: func() time.Time { return now }})
	queue.SetImmichService(NewImmichService(db, settings), nil)
	device := model.Device{Name: "frame"}
	require.NoError(t, db.Create(&device).Error)
	gallery := model.Image{Source: model.SourceGallery, FilePath: "photo.jpg"}
	immich := model.Image{Source: model.SourceImmich, ExternalID: "asset"}
	require.NoError(t, db.Create(&[]model.Image{gallery, immich}).Error)
	// Reload assigned IDs after slice creation.
	var images []model.Image
	require.NoError(t, db.Order("id").Find(&images).Error)
	gallery, immich = images[len(images)-2], images[len(images)-1]
	expired := now.Add(-time.Second)
	galleryLease := model.DeviceQueueItem{DeviceID: device.ID, ImageID: gallery.ID, Position: 10, Source: gallery.Source, State: model.QueueStateLeased, LeaseExpiresAt: &expired}
	immichPending := model.DeviceQueueItem{DeviceID: device.ID, ImageID: immich.ID, Position: 20, Source: immich.Source, State: model.QueueStatePending}
	require.NoError(t, db.Create(&galleryLease).Error)
	require.NoError(t, db.Create(&immichPending).Error)

	require.NoError(t, queue.CompleteDelivery(device.ID, galleryLease.ID, expired))
	require.ErrorIs(t, db.First(&model.DeviceQueueItem{}, galleryLease.ID).Error, gorm.ErrRecordNotFound)
	require.Error(t, queue.Remove(device.ID, immichPending.ID))
	require.Equal(t, model.QueueStatePending, loadQueueLifecycleItem(t, db, immichPending.ID).State)

	galleryPending := model.DeviceQueueItem{DeviceID: device.ID, ImageID: gallery.ID, Position: 30, Source: gallery.Source, State: model.QueueStatePending}
	require.NoError(t, db.Create(&galleryPending).Error)
	removed, err := queue.Clear(device.ID)
	require.Error(t, err)
	require.Zero(t, removed)
	require.NoError(t, db.First(&model.DeviceQueueItem{}, galleryPending.ID).Error)
	require.NoError(t, db.First(&model.DeviceQueueItem{}, immichPending.ID).Error)
	require.NoError(t, queue.Remove(device.ID, galleryPending.ID), "non-Immich removal must ignore malformed Immich policy")
}

func TestQueueCompletionFinalUnlinkFailureUsesDurableTombstoneRecovery(t *testing.T) {
	db := dateIntegrationDB(t)
	settings := NewSettingsService(db)
	require.NoError(t, settings.SetImmichDatePair("2025-01-01", ""))
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	queue := NewQueueServiceWithOptions(db, QueueServiceOptions{Now: func() time.Time { return now }})
	queue.SetImmichService(NewImmichService(db, settings), nil)
	device := model.Device{Name: "frame"}
	require.NoError(t, db.Create(&device).Error)
	date := "2024-01-01"
	imageRow := model.Image{Source: model.SourceImmich, ExternalID: "asset", PhotoTakenDate: &date}
	require.NoError(t, db.Create(&imageRow).Error)
	expired := now.Add(-time.Second)
	item := model.DeviceQueueItem{DeviceID: device.ID, ImageID: imageRow.ID, Position: 10, Source: imageRow.Source, State: model.QueueStateLeased, LeaseExpiresAt: &expired}
	require.NoError(t, db.Create(&item).Error)
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "cache.jpg")
	require.NoError(t, os.WriteFile(cachePath, []byte("cached"), 0o600))
	require.NoError(t, db.Create(&model.ImmichCache{ImageID: imageRow.ID, AssetID: "asset", FilePath: cachePath}).Error)
	originalRemove := removeStagedCacheFilesFn
	removeStagedCacheFilesFn = func([]stagedCacheFile) error { return errors.New("injected final unlink failure") }
	t.Cleanup(func() { removeStagedCacheFilesFn = originalRemove })

	require.NoError(t, queue.CompleteDelivery(device.ID, item.ID, expired))
	require.ErrorIs(t, db.First(&model.DeviceQueueItem{}, item.ID).Error, gorm.ErrRecordNotFound)
	require.FileExists(t, cachePath+".deleting")
	require.NoError(t, recoverStagedCacheFiles(db, dir))
	require.NoFileExists(t, cachePath+".deleting")
}

func loadQueueLifecycleItem(t *testing.T, db *gorm.DB, id uint) model.DeviceQueueItem {
	t.Helper()
	var item model.DeviceQueueItem
	require.NoError(t, db.First(&item, id).Error)
	return item
}
