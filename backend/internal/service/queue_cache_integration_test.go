package service

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/aitjcize/esp32-photoframe-server/backend/internal/model"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestQueueLoaderClassifiesInvalidReferences(t *testing.T) {
	loader := NewQueueImageLoader(nil, t.TempDir(), nil, nil, nil)
	tests := []struct {
		name string
		item *model.DeviceQueueItem
		code string
	}{
		{name: "missing relation", item: &model.DeviceQueueItem{Source: model.SourceImmich}, code: "missing_image_relation"},
		{name: "source mismatch", item: &model.DeviceQueueItem{Source: model.SourceImmich, Image: &model.Image{Source: model.SourceGallery}}, code: "source_mismatch"},
		{name: "missing identifier", item: &model.DeviceQueueItem{Source: model.SourceImmich, Image: &model.Image{Source: model.SourceImmich}}, code: "missing_source_identifier"},
		{name: "stale URL", item: &model.DeviceQueueItem{Source: model.SourceUnsplash, Image: &model.Image{Source: model.SourceUnsplash, FilePath: "://bad"}}, code: "invalid_source_identifier"},
		{name: "unsupported source", item: &model.DeviceQueueItem{Source: "removed", Image: &model.Image{Source: "removed"}}, code: "unsupported_source"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := loader.Load(test.item)
			var failure *QueueFailure
			require.ErrorAs(t, err, &failure)
			require.Equal(t, QueueFailurePermanentInvalid, failure.Category)
			require.Equal(t, test.code, failure.Code)
		})
	}
}

func TestOfflineImmichQueueLoadIsRetryable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "offline", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	db := dateIntegrationDB(t)
	settings := NewSettingsService(db)
	require.NoError(t, settings.Set("immich_url", server.URL))
	require.NoError(t, settings.Set("immich_api_key", "key"))
	immich := NewImmichService(db, settings)
	imageRow := model.Image{Source: model.SourceImmich, ExternalID: "asset"}
	require.NoError(t, db.Create(&imageRow).Error)
	device := model.Device{Name: "frame"}
	require.NoError(t, db.Create(&device).Error)
	queue := NewQueueService(db)
	queue.SetImmichService(immich, nil)
	items, _, _, err := queue.Add(device.ID, []uint{imageRow.ID})
	require.NoError(t, err)
	claim, err := queue.Poll(device.ID)
	require.NoError(t, err)
	require.Equal(t, items[0].ID, claim.Item.ID)

	_, err = NewQueueImageLoader(db, t.TempDir(), immich, nil, nil).Load(claim.Item)
	var failure *QueueFailure
	require.ErrorAs(t, err, &failure)
	require.Equal(t, QueueFailureRetryable, failure.Category)
	require.Equal(t, "upstream_unavailable", failure.Code)
	require.NoError(t, queue.FailClaim(device.ID, claim.Item.ID, *claim.Item.ClaimExpiresAt, err))
	failed := loadQueueLifecycleItem(t, db, claim.Item.ID)
	require.Equal(t, model.QueueStateFailed, failed.State)
	require.Equal(t, "upstream_unavailable", *failed.LastErrorCode)
}

func TestQueueCachePruningProtectsEveryStoredState(t *testing.T) {
	db := dateIntegrationDB(t)
	settings := NewSettingsService(db)
	require.NoError(t, settings.Set("immich_cache_max_images", "5"))
	immich := NewImmichService(db, settings)
	cache := NewImmichCacheService(db, settings, immich, t.TempDir())
	device := model.Device{Name: "frame"}
	require.NoError(t, db.Create(&device).Error)
	states := []string{model.QueueStatePending, model.QueueStateClaimed, model.QueueStateLeased, model.QueueStateFailed, model.QueueStatePermanentlyInvalid}
	for i, state := range states {
		img := model.Image{Source: model.SourceImmich, ExternalID: state}
		require.NoError(t, db.Create(&img).Error)
		path := filepath.Join(t.TempDir(), state+".jpg")
		require.NoError(t, os.WriteFile(path, testJPEG(t), 0o600))
		require.NoError(t, db.Create(&model.ImmichCache{ImageID: img.ID, AssetID: state, FilePath: path, CachedAt: time.Unix(int64(i), 0)}).Error)
		require.NoError(t, db.Create(&model.DeviceQueueItem{DeviceID: device.ID, ImageID: img.ID, Source: model.SourceImmich, State: state, Position: (i + 1) * 10}).Error)
	}
	victim := model.Image{Source: model.SourceImmich, ExternalID: "victim"}
	require.NoError(t, db.Create(&victim).Error)
	victimPath := filepath.Join(t.TempDir(), "victim.jpg")
	require.NoError(t, os.WriteFile(victimPath, testJPEG(t), 0o600))
	require.NoError(t, db.Create(&model.ImmichCache{ImageID: victim.ID, AssetID: "victim", FilePath: victimPath, CachedAt: time.Now()}).Error)

	cache.prune()
	for _, state := range states {
		var count int64
		require.NoError(t, db.Model(&model.ImmichCache{}).Where("asset_id = ?", state).Count(&count).Error)
		require.EqualValues(t, 1, count, state)
	}
	require.ErrorIs(t, db.Where("asset_id = ?", "victim").First(&model.ImmichCache{}).Error, gorm.ErrRecordNotFound)
	require.NoFileExists(t, victimPath)
}

func TestCorruptQueueCacheFallsBackAndRepairs(t *testing.T) {
	jpegData := testJPEG(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(jpegData) }))
	defer server.Close()
	db := dateIntegrationDB(t)
	settings := NewSettingsService(db)
	require.NoError(t, settings.Set("immich_url", server.URL))
	require.NoError(t, settings.Set("immich_api_key", "key"))
	installFakeMagick(t)
	immich := NewImmichService(db, settings)
	cache := NewImmichCacheService(db, settings, immich, t.TempDir())
	imageRow := model.Image{Source: model.SourceImmich, ExternalID: "asset"}
	require.NoError(t, db.Create(&imageRow).Error)
	device := model.Device{Name: "frame"}
	require.NoError(t, db.Create(&device).Error)
	item := model.DeviceQueueItem{DeviceID: device.ID, ImageID: imageRow.ID, Source: model.SourceImmich, State: model.QueueStatePending, Position: 10, Image: &imageRow}
	require.NoError(t, db.Create(&item).Error)
	corruptPath := filepath.Join(t.TempDir(), "corrupt.jpg")
	require.NoError(t, os.WriteFile(corruptPath, []byte("not an image"), 0o600))
	require.NoError(t, db.Create(&model.ImmichCache{ImageID: imageRow.ID, AssetID: "asset", FilePath: corruptPath}).Error)

	loaded, err := NewQueueImageLoader(db, t.TempDir(), immich, cache, nil).Load(&item)
	require.NoError(t, err)
	require.NotNil(t, loaded)
	require.Eventually(t, func() bool {
		_, oldErr := os.Stat(corruptPath)
		return cache.QueueCacheStatus(imageRow.ID) == "ready" && os.IsNotExist(oldErr)
	}, 3*time.Second, 20*time.Millisecond)

	missing := model.Image{Source: model.SourceImmich, ExternalID: "missing-cache"}
	require.NoError(t, db.Create(&missing).Error)
	missingItem := model.DeviceQueueItem{DeviceID: device.ID, ImageID: missing.ID, Source: model.SourceImmich, State: model.QueueStatePending, Position: 20, Image: &missing}
	require.NoError(t, db.Create(&missingItem).Error)
	require.Equal(t, "missing", cache.QueueCacheStatus(missing.ID))
	_, err = NewQueueImageLoader(db, t.TempDir(), immich, cache, nil).Load(&missingItem)
	require.NoError(t, err)
	require.Eventually(t, func() bool { return cache.QueueCacheStatus(missing.ID) == "ready" }, 3*time.Second, 20*time.Millisecond)
}

func TestQueuePrecacheRunsWithGeneralCacheEnabledAndDisabled(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(strconv.FormatBool(enabled), func(t *testing.T) {
			jpegData := testJPEG(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(jpegData) }))
			defer server.Close()
			db := dateIntegrationDB(t)
			settings := NewSettingsService(db)
			require.NoError(t, settings.Set("immich_url", server.URL))
			require.NoError(t, settings.Set("immich_api_key", "key"))
			require.NoError(t, settings.Set("immich_cache_enabled", strconv.FormatBool(enabled)))
			installFakeMagick(t)
			immich := NewImmichService(db, settings)
			cache := NewImmichCacheService(db, settings, immich, t.TempDir())
			require.Equal(t, enabled, cache.Enabled())
			device := model.Device{Name: "frame"}
			require.NoError(t, db.Create(&device).Error)
			imageRow := model.Image{Source: model.SourceImmich, ExternalID: "asset"}
			require.NoError(t, db.Create(&imageRow).Error)
			require.NoError(t, db.Create(&model.DeviceQueueItem{
				DeviceID: device.ID, ImageID: imageRow.ID, Source: imageRow.Source,
				State: model.QueueStatePending, Position: 10,
			}).Error)

			require.NoError(t, cache.CacheForQueue(imageRow.ID))
			require.Equal(t, "ready", cache.QueueCacheStatus(imageRow.ID), "queue pre-cache must be independent of general cache mode")
		})
	}
}

func TestImmichCacheConstructorRecoversStagedFilesAcrossRestart(t *testing.T) {
	db := dateIntegrationDB(t)
	settings := NewSettingsService(db)
	immich := NewImmichService(db, settings)
	dataDir := t.TempDir()
	cacheDir := filepath.Join(dataDir, immichCacheDirName)
	require.NoError(t, os.MkdirAll(cacheDir, 0o755))
	imageRow := model.Image{Source: model.SourceImmich, ExternalID: "asset"}
	require.NoError(t, db.Create(&imageRow).Error)
	original := filepath.Join(cacheDir, "asset.jpg")
	staged := original + ".deleting"
	require.NoError(t, os.WriteFile(staged, testJPEG(t), 0o600))
	cacheRow := model.ImmichCache{ImageID: imageRow.ID, AssetID: "asset", FilePath: original}
	require.NoError(t, db.Create(&cacheRow).Error)

	first := NewImmichCacheService(db, settings, immich, dataDir)
	require.Equal(t, "ready", first.QueueCacheStatus(imageRow.ID))
	require.NoFileExists(t, staged)

	require.NoError(t, os.Rename(original, staged))
	require.NoError(t, db.Unscoped().Delete(&cacheRow).Error)
	second := NewImmichCacheService(db, settings, immich, dataDir)
	require.Empty(t, second.Lookup(imageRow.ID))
	require.NoFileExists(t, original)
	require.NoFileExists(t, staged, "restart recovery must remove a tombstone whose database row committed as deleted")
}

func TestUpstreamDecodeCorruptionRetriesThenSucceeds(t *testing.T) {
	var requests int
	jpegData := testJPEG(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		if requests == 1 {
			_, _ = w.Write([]byte("corrupt"))
			return
		}
		_, _ = w.Write(jpegData)
	}))
	defer server.Close()
	imageRow := model.Image{Source: model.SourceUnsplash, FilePath: server.URL}
	item := model.DeviceQueueItem{Source: model.SourceUnsplash, Image: &imageRow}
	loader := NewQueueImageLoader(nil, t.TempDir(), nil, nil, nil)

	_, err := loader.Load(&item)
	var failure *QueueFailure
	require.ErrorAs(t, err, &failure)
	require.Equal(t, QueueFailureRetryable, failure.Category)
	require.Equal(t, "upstream_invalid_image", failure.Code)
	loaded, err := loader.Load(&item)
	require.NoError(t, err)
	require.NotNil(t, loaded)
}

func TestQueuePolicyChangesAndInvalidPolicyPreserveOccurrences(t *testing.T) {
	db := dateIntegrationDB(t)
	settings := NewSettingsService(db)
	require.NoError(t, settings.SetImmichDatePair("2024-01-01", "2024-12-31"))
	immich := NewImmichService(db, settings)
	queue := NewQueueService(db)
	queue.SetImmichService(immich, nil)
	device := model.Device{Name: "frame"}
	require.NoError(t, db.Create(&device).Error)
	date := "2024-06-01"
	imageRow := model.Image{Source: model.SourceImmich, ExternalID: "asset", PhotoTakenDate: &date}
	require.NoError(t, db.Create(&imageRow).Error)
	items, _, _, err := queue.Add(device.ID, []uint{imageRow.ID})
	require.NoError(t, err)

	require.NoError(t, settings.SetImmichDatePair("2025-01-01", ""))
	cachePath := filepath.Join(t.TempDir(), "cached.jpg")
	require.NoError(t, os.WriteFile(cachePath, testJPEG(t), 0o600))
	require.NoError(t, db.Create(&model.ImmichCache{ImageID: imageRow.ID, AssetID: "asset", FilePath: cachePath}).Error)
	cache := NewImmichCacheService(db, settings, immich, t.TempDir())
	items[0].Image = &imageRow
	_, err = NewQueueImageLoader(db, t.TempDir(), immich, cache, nil).Load(&items[0])
	require.NoError(t, err, "a valid policy change after enqueue must not revoke the queue override")

	require.NoError(t, settings.Set("immich_date_from", "invalid"))
	_, err = queue.Poll(device.ID)
	var policyErr *ImmichDatePolicyError
	require.ErrorAs(t, err, &policyErr)
	stored := loadQueueLifecycleItem(t, db, items[0].ID)
	require.Equal(t, model.QueueStatePending, stored.State)
	require.Zero(t, stored.AttemptCount)
}

func TestClaimPolicySnapshotSurvivesChangeBeforeLoad(t *testing.T) {
	jpegData := testJPEG(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(jpegData) }))
	defer server.Close()
	db := dateIntegrationDB(t)
	settings := NewSettingsService(db)
	require.NoError(t, settings.SetImmichDatePair("2024-01-01", "2024-12-31"))
	require.NoError(t, settings.Set("immich_url", server.URL))
	require.NoError(t, settings.Set("immich_api_key", "key"))
	immich := NewImmichService(db, settings)
	queue := NewQueueService(db)
	queue.SetImmichService(immich, nil)
	device := model.Device{Name: "frame"}
	require.NoError(t, db.Create(&device).Error)
	imageRow := model.Image{Source: model.SourceImmich, ExternalID: "asset"}
	require.NoError(t, db.Create(&imageRow).Error)
	items, _, _, err := queue.Add(device.ID, []uint{imageRow.ID})
	require.NoError(t, err)

	claim, err := queue.Poll(device.ID)
	require.NoError(t, err)
	require.True(t, claim.Item.PolicyValidated)
	beforeLoad := loadQueueLifecycleItem(t, db, items[0].ID)
	require.NoError(t, settings.Set("immich_date_from", "invalid"))
	loaded, err := NewQueueImageLoader(db, t.TempDir(), immich, nil, nil).Load(claim.Item)
	require.NoError(t, err)
	require.NotNil(t, loaded)
	afterLoad := loadQueueLifecycleItem(t, db, items[0].ID)
	require.Equal(t, beforeLoad.State, afterLoad.State)
	require.Equal(t, beforeLoad.ClaimExpiresAt, afterLoad.ClaimExpiresAt)
	require.Equal(t, beforeLoad.LeaseExpiresAt, afterLoad.LeaseExpiresAt)
	require.Equal(t, beforeLoad.AttemptCount, afterLoad.AttemptCount)
	require.Equal(t, beforeLoad.NextAttemptAt, afterLoad.NextAttemptAt)
	require.Equal(t, beforeLoad.LastAttemptAt, afterLoad.LastAttemptAt)
	require.Equal(t, beforeLoad.LastErrorCode, afterLoad.LastErrorCode)
	require.Equal(t, beforeLoad.LastError, afterLoad.LastError)
}

func TestSourceSyncRetainsInvalidQueueImageCacheAndThumbnail(t *testing.T) {
	db := dateIntegrationDB(t)
	device := model.Device{Name: "frame"}
	require.NoError(t, db.Create(&device).Error)
	imageRow := model.Image{Source: model.SourceImmich, ExternalID: "asset"}
	require.NoError(t, db.Create(&imageRow).Error)
	require.NoError(t, db.Create(&model.DeviceQueueItem{DeviceID: device.ID, ImageID: imageRow.ID, Source: model.SourceImmich, State: model.QueueStatePermanentlyInvalid, Position: 10}).Error)
	cachePath := filepath.Join(t.TempDir(), "cache.jpg")
	require.NoError(t, os.WriteFile(cachePath, testJPEG(t), 0o600))
	require.NoError(t, db.Create(&model.ImmichCache{ImageID: imageRow.ID, AssetID: "asset", FilePath: cachePath}).Error)
	oldThumbnailDir := thumbnailDir
	thumbnailDir = t.TempDir()
	t.Cleanup(func() { thumbnailDir = oldThumbnailDir })
	thumbPath := filepath.Join(thumbnailDir, strconv.FormatUint(uint64(imageRow.ID), 10)+".jpg")
	require.NoError(t, os.WriteFile(thumbPath, testJPEG(t), 0o600))

	gcOrphanImagesForSource(db, model.SourceImmich)
	require.NoError(t, db.First(&model.Image{}, imageRow.ID).Error)
	require.NoError(t, db.First(&model.ImmichCache{}, "image_id = ?", imageRow.ID).Error)
	require.FileExists(t, cachePath)
	require.FileExists(t, thumbPath)
}

func TestSourceClearRetainsImagesReferencedByEveryQueueState(t *testing.T) {
	db := dateIntegrationDB(t)
	device := model.Device{Name: "frame"}
	require.NoError(t, db.Create(&device).Error)
	states := []string{model.QueueStatePending, model.QueueStateClaimed, model.QueueStateLeased, model.QueueStateFailed, model.QueueStatePermanentlyInvalid}
	var protectedIDs []uint
	for i, state := range states {
		imageRow := model.Image{Source: model.SourceImmich, ExternalID: state}
		require.NoError(t, db.Create(&imageRow).Error)
		protectedIDs = append(protectedIDs, imageRow.ID)
		require.NoError(t, db.Create(&model.DeviceQueueItem{
			DeviceID: device.ID, ImageID: imageRow.ID, Position: (i + 1) * 10,
			Source: imageRow.Source, State: state,
		}).Error)
	}
	unreferenced := model.Image{Source: model.SourceImmich, ExternalID: "delete-me"}
	require.NoError(t, db.Create(&unreferenced).Error)

	require.NoError(t, clearSourcePhotos(db, model.SourceImmich))
	var count int64
	require.NoError(t, db.Model(&model.Image{}).Where("id IN ?", protectedIDs).Count(&count).Error)
	require.EqualValues(t, len(protectedIDs), count)
	require.ErrorIs(t, db.First(&model.Image{}, unreferenced.ID).Error, gorm.ErrRecordNotFound)
}

func TestFinalReferenceCleanupHandlesDuplicatesDevicesAndClearFailure(t *testing.T) {
	db := dateIntegrationDB(t)
	settings := NewSettingsService(db)
	require.NoError(t, settings.SetImmichDatePair("2025-01-01", ""))
	queue := NewQueueService(db)
	queue.SetImmichService(NewImmichService(db, settings), nil)
	d1, d2 := model.Device{Name: "one"}, model.Device{Name: "two"}
	require.NoError(t, db.Create(&d1).Error)
	require.NoError(t, db.Create(&d2).Error)
	date := "2024-01-01"
	imageRow := model.Image{Source: model.SourceImmich, ExternalID: "asset", PhotoTakenDate: &date}
	require.NoError(t, db.Create(&imageRow).Error)
	first, _, _, err := queue.Add(d1.ID, []uint{imageRow.ID, imageRow.ID})
	require.NoError(t, err)
	second, _, _, err := queue.Add(d2.ID, []uint{imageRow.ID})
	require.NoError(t, err)
	require.NoError(t, queue.Remove(d1.ID, first[0].ID))
	require.NoError(t, queue.Remove(d1.ID, first[1].ID))
	require.NoError(t, db.First(&model.Image{}, imageRow.ID).Error)

	badPath := filepath.Join(t.TempDir(), "directory")
	require.NoError(t, os.Mkdir(badPath, 0o755))
	require.NoError(t, db.Create(&model.ImmichCache{ImageID: imageRow.ID, AssetID: "asset", FilePath: badPath}).Error)
	_, err = queue.Clear(d2.ID)
	require.Error(t, err)
	require.NoError(t, db.First(&model.DeviceQueueItem{}, second[0].ID).Error)
	require.NoError(t, db.First(&model.Image{}, imageRow.ID).Error)
}

func testJPEG(t *testing.T) []byte {
	t.Helper()
	var data bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.White)
	require.NoError(t, jpeg.Encode(&data, img, nil))
	return data.Bytes()
}

func installFakeMagick(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	script := filepath.Join(bin, "magick")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nfor last; do :; done\ncp \"$1\" \"$last\"\n"), 0o755))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}
