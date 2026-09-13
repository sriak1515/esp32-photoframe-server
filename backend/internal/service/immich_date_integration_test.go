package service

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aitjcize/esp32-photoframe-server/backend/internal/model"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestImmichSourceModesLocallyFilterOverReturnedAssets(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		items := `[{"id":"inside","type":"IMAGE","originalFileName":"inside.jpg","localDateTime":"2024-01-15T12:00:00"},{"id":"outside","type":"IMAGE","originalFileName":"outside.jpg","localDateTime":"2024-02-01T12:00:00"}]`
		if r.URL.Path == "/api/memories" {
			_, _ = w.Write([]byte(`[{"id":"lane","assets":` + items + `,"data":{"year":2024}}]`))
			return
		}
		_, _ = w.Write([]byte(`{"assets":{"count":2,"total":2,"items":` + items + `}}`))
	}))
	defer server.Close()
	db := dateIntegrationDB(t)
	settings := NewSettingsService(db)
	require.NoError(t, settings.Set("immich_url", server.URL))
	require.NoError(t, settings.Set("immich_api_key", "key"))
	require.NoError(t, settings.SetImmichDatePair("2024-01-01", "2024-01-31"))
	svc := NewImmichService(db, settings)
	policy, err := svc.DatePolicy()
	require.NoError(t, err)
	for _, externalID := range []string{model.ImmichVirtualAll, model.ImmichVirtualFavorites, model.ImmichVirtualMemories} {
		assets, err := svc.fetchAlbumAssets(model.Album{ExternalID: externalID, Kind: model.AlbumKindVirtual}, policy)
		require.NoError(t, err, externalID)
		require.Len(t, assets, 1, externalID)
		require.Equal(t, "inside", assets[0].ExternalID)
	}
}

func TestInvalidPolicyAbortsSyncBeforeImmichRequest(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.WriteHeader(http.StatusOK) }))
	defer server.Close()
	db := dateIntegrationDB(t)
	settings := NewSettingsService(db)
	require.NoError(t, settings.Set("immich_url", server.URL))
	require.NoError(t, settings.Set("immich_api_key", "key"))
	require.NoError(t, settings.Set("immich_date_from", "bad"))
	require.Error(t, NewImmichService(db, settings).ImportPhotos())
	require.Zero(t, requests.Load())
}

func TestFetchAlbumAssetsPersistsCorrectedOutOfRangeMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"album","assetCount":1,"assets":[{"id":"x","type":"IMAGE","originalFileName":"x.jpg","localDateTime":"2024-02-01T12:00:00","exifInfo":{"dateTimeOriginal":"2024-02-01T12:00:00"}}]}`))
	}))
	defer server.Close()
	db := dateIntegrationDB(t)
	settings := NewSettingsService(db)
	require.NoError(t, settings.Set("immich_url", server.URL))
	require.NoError(t, settings.Set("immich_api_key", "key"))
	require.NoError(t, settings.SetImmichDatePair("2024-01-01", "2024-01-31"))
	oldDate := "2024-01-15"
	image := model.Image{Source: model.SourceImmich, ExternalID: "x", PhotoTakenDate: &oldDate}
	require.NoError(t, db.Create(&image).Error)
	svc := NewImmichService(db, settings)
	policy, err := svc.DatePolicy()
	require.NoError(t, err)
	assets, err := svc.fetchAlbumAssets(model.Album{ExternalID: "album", Kind: model.AlbumKindReal}, policy)
	require.NoError(t, err)
	require.Empty(t, assets)
	require.NoError(t, db.First(&image, image.ID).Error)
	require.Equal(t, "2024-02-01", *image.PhotoTakenDate)
}

func dateIntegrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Setting{}, &model.Image{}, &model.Album{}, &model.ImageAlbumMembership{}, &model.Device{}, &model.DeviceQueueItem{}, &model.ImmichCache{}))
	return db
}

func TestUpsertAlbumAssetsRefreshesKnownDatesAndPreservesOmittedDates(t *testing.T) {
	db := dateIntegrationDB(t)
	album := model.Album{Source: model.SourceImmich, ExternalID: "a", Name: "a"}
	require.NoError(t, db.Create(&album).Error)
	oldTime := time.Date(2024, 1, 1, 1, 0, 0, 0, time.UTC)
	oldDate := "2024-01-01"
	image := model.Image{Source: model.SourceImmich, ExternalID: "x", PhotoTakenAt: &oldTime, PhotoTakenDate: &oldDate}
	require.NoError(t, db.Create(&image).Error)
	newTime := oldTime.AddDate(0, 0, 2)
	newDate := "2024-01-03"
	_, _, err := upsertAlbumAssets(db, model.SourceImmich, album.ID, []RemoteAsset{{ExternalID: "x", PhotoTakenAt: &newTime, PhotoTakenDate: &newDate}})
	require.NoError(t, err)
	require.NoError(t, db.First(&image, image.ID).Error)
	require.Equal(t, newDate, *image.PhotoTakenDate)
	require.True(t, image.PhotoTakenAt.Equal(newTime))
	_, _, err = upsertAlbumAssets(db, model.SourceImmich, album.ID, []RemoteAsset{{ExternalID: "x"}})
	require.NoError(t, err)
	require.NoError(t, db.First(&image, image.ID).Error)
	require.Equal(t, newDate, *image.PhotoTakenDate)
}

func TestQueueReferenceProtectsOrphanAndFinalRemovalCleansCache(t *testing.T) {
	db := dateIntegrationDB(t)
	settings := NewSettingsService(db)
	require.NoError(t, settings.SetImmichDatePair("2024-02-01", ""))
	immich := NewImmichService(db, settings)
	queue := NewQueueService(db)
	queue.SetImmichService(immich, nil)
	device := model.Device{Name: "frame"}
	require.NoError(t, db.Create(&device).Error)
	date := "2024-01-01"
	image := model.Image{Source: model.SourceImmich, ExternalID: "x", PhotoTakenDate: &date}
	require.NoError(t, db.Create(&image).Error)
	items, _, err := queue.Add(device.ID, []uint{image.ID})
	require.NoError(t, err)
	require.Len(t, items, 1)
	cachePath := filepath.Join(t.TempDir(), "cached.jpg")
	require.NoError(t, os.WriteFile(cachePath, []byte("x"), 0600))
	require.NoError(t, db.Create(&model.ImmichCache{ImageID: image.ID, AssetID: "x", FilePath: cachePath, CachedAt: time.Now()}).Error)
	gcOrphanImagesForSource(db, model.SourceImmich)
	require.NoError(t, db.First(&image, image.ID).Error)
	require.NoError(t, queue.Remove(device.ID, items[0].ID))
	require.ErrorIs(t, db.First(&model.Image{}, image.ID).Error, gorm.ErrRecordNotFound)
	_, err = os.Stat(cachePath)
	require.True(t, os.IsNotExist(err))
}

func TestInvalidPolicyBlocksImmichQueueWithoutChangingEntries(t *testing.T) {
	db := dateIntegrationDB(t)
	settings := NewSettingsService(db)
	require.NoError(t, settings.Set("immich_date_from", "2024-02-01"))
	require.NoError(t, settings.Set("immich_date_to", "2024-01-01"))
	queue := NewQueueService(db)
	queue.SetImmichService(NewImmichService(db, settings), nil)
	device := model.Device{Name: "frame"}
	require.NoError(t, db.Create(&device).Error)
	image := model.Image{Source: model.SourceImmich, ExternalID: "x"}
	require.NoError(t, db.Create(&image).Error)
	_, _, err := queue.Add(device.ID, []uint{image.ID})
	require.Error(t, err)
	count, err := queue.Count(device.ID)
	require.NoError(t, err)
	require.Zero(t, count)
}

func TestQueueAwareGCIsImmichOnly(t *testing.T) {
	db := dateIntegrationDB(t)
	device := model.Device{Name: "frame"}
	require.NoError(t, db.Create(&device).Error)
	image := model.Image{Source: model.SourceSynologyPhotos, ExternalID: "x"}
	require.NoError(t, db.Create(&image).Error)
	require.NoError(t, db.Create(&model.DeviceQueueItem{DeviceID: device.ID, ImageID: image.ID, Position: 10, Source: image.Source}).Error)
	gcOrphanImagesForSource(db, model.SourceSynologyPhotos)
	require.ErrorIs(t, db.First(&model.Image{}, image.ID).Error, gorm.ErrRecordNotFound)
}

func TestQueueCleanupSurfacesDatabaseErrors(t *testing.T) {
	db := dateIntegrationDB(t)
	settings := NewSettingsService(db)
	require.NoError(t, settings.SetImmichDatePair("2024-02-01", ""))
	queue := NewQueueService(db)
	queue.SetImmichService(NewImmichService(db, settings), nil)
	device := model.Device{Name: "frame"}
	require.NoError(t, db.Create(&device).Error)
	date := "2024-01-01"
	image := model.Image{Source: model.SourceImmich, ExternalID: "x", PhotoTakenDate: &date}
	require.NoError(t, db.Create(&image).Error)
	items, _, err := queue.Add(device.ID, []uint{image.ID})
	require.NoError(t, err)
	cachePath := filepath.Join(t.TempDir(), "required.jpg")
	require.NoError(t, os.WriteFile(cachePath, []byte("required"), 0600))
	require.NoError(t, db.Create(&model.ImmichCache{ImageID: image.ID, AssetID: "x", FilePath: cachePath, CachedAt: time.Now()}).Error)
	require.NoError(t, db.Migrator().DropTable(&model.ImmichCache{}))
	require.Error(t, queue.Remove(device.ID, items[0].ID))
	count, err := queue.Count(device.ID)
	require.NoError(t, err)
	require.EqualValues(t, 1, count)
	require.NoError(t, db.First(&model.Image{}, image.ID).Error)
	bytes, err := os.ReadFile(cachePath)
	require.NoError(t, err)
	require.Equal(t, []byte("required"), bytes)
}

func TestCachedPolicyPickerUsesQualifiedColumns(t *testing.T) {
	db := dateIntegrationDB(t)
	date := "2024-01-01"
	image := model.Image{Source: model.SourceImmich, ExternalID: "x", PhotoTakenDate: &date}
	require.NoError(t, db.Create(&image).Error)
	require.NoError(t, db.Create(&model.ImmichCache{ImageID: image.ID, AssetID: "x", FilePath: "/tmp/x", CachedAt: time.Now()}).Error)
	p, err := NewImmichDatePolicy("2024-01-01", "2024-01-01")
	require.NoError(t, err)
	got, err := pickRandomDBPhotoWithPolicy(db, model.SourceImmich, "", []uint{999}, p, true)
	require.NoError(t, err)
	require.Equal(t, image.ID, got.ID)
	got, err = PickRandomDBPhotoFiltered(db, model.SourceImmich, "", []uint{999}, time.Time{}, time.Time{}, true)
	require.NoError(t, err)
	require.Equal(t, image.ID, got.ID)
}

func TestFinalQueueReferenceControlsCleanup(t *testing.T) {
	db := dateIntegrationDB(t)
	settings := NewSettingsService(db)
	require.NoError(t, settings.SetImmichDatePair("2024-02-01", ""))
	queue := NewQueueService(db)
	queue.SetImmichService(NewImmichService(db, settings), nil)
	d1, d2 := model.Device{Name: "one"}, model.Device{Name: "two"}
	require.NoError(t, db.Create(&d1).Error)
	require.NoError(t, db.Create(&d2).Error)
	date := "2024-01-01"
	image := model.Image{Source: model.SourceImmich, ExternalID: "shared", PhotoTakenDate: &date}
	require.NoError(t, db.Create(&image).Error)
	i1, _, err := queue.Add(d1.ID, []uint{image.ID})
	require.NoError(t, err)
	_, _, err = queue.Add(d2.ID, []uint{image.ID})
	require.NoError(t, err)
	require.NoError(t, queue.Remove(d1.ID, i1[0].ID))
	require.NoError(t, db.First(&model.Image{}, image.ID).Error)
	_, err = queue.Clear(d2.ID)
	require.NoError(t, err)
	require.ErrorIs(t, db.First(&model.Image{}, image.ID).Error, gorm.ErrRecordNotFound)
}

func TestQueueCleanupFilesystemFailurePreservesDatabase(t *testing.T) {
	db := dateIntegrationDB(t)
	settings := NewSettingsService(db)
	require.NoError(t, settings.SetImmichDatePair("2024-02-01", ""))
	queue := NewQueueService(db)
	queue.SetImmichService(NewImmichService(db, settings), nil)
	device := model.Device{Name: "frame"}
	require.NoError(t, db.Create(&device).Error)
	date := "2024-01-01"
	image := model.Image{Source: model.SourceImmich, ExternalID: "x", PhotoTakenDate: &date}
	require.NoError(t, db.Create(&image).Error)
	items, _, err := queue.Add(device.ID, []uint{image.ID})
	require.NoError(t, err)
	directory := filepath.Join(t.TempDir(), "not-a-file")
	require.NoError(t, os.Mkdir(directory, 0o755))
	cache := model.ImmichCache{ImageID: image.ID, AssetID: "x", FilePath: directory}
	require.NoError(t, db.Create(&cache).Error)
	require.Error(t, queue.Remove(device.ID, items[0].ID))
	count, err := queue.Count(device.ID)
	require.NoError(t, err)
	require.EqualValues(t, 1, count)
	require.NoError(t, db.First(&model.Image{}, image.ID).Error)
	require.NoError(t, db.First(&model.ImmichCache{}, cache.ID).Error)
}
