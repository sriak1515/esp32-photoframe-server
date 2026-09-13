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
	"testing"
	"time"

	"github.com/aitjcize/esp32-photoframe-server/backend/internal/model"
	"github.com/stretchr/testify/require"
)

func TestRecoverStagedCacheFiles(t *testing.T) {
	db := dateIntegrationDB(t)
	dir := t.TempDir()
	liveOriginal := filepath.Join(dir, "live.jpg")
	orphanOriginal := filepath.Join(dir, "orphan.jpg")
	require.NoError(t, os.WriteFile(liveOriginal+".deleting", []byte("live"), 0o600))
	require.NoError(t, os.WriteFile(orphanOriginal+".deleting", []byte("orphan"), 0o600))
	image := model.Image{Source: model.SourceImmich, ExternalID: "live"}
	require.NoError(t, db.Create(&image).Error)
	require.NoError(t, db.Create(&model.ImmichCache{ImageID: image.ID, AssetID: "live", FilePath: liveOriginal}).Error)
	require.NoError(t, recoverStagedCacheFiles(db, dir))
	require.FileExists(t, liveOriginal)
	require.NoFileExists(t, liveOriginal+".deleting")
	require.NoFileExists(t, orphanOriginal+".deleting")
}

func TestImmichOrphanGCSerializesWithCacheFilesystemWrites(t *testing.T) {
	db := dateIntegrationDB(t)
	image := model.Image{Source: model.SourceImmich, ExternalID: "x"}
	require.NoError(t, db.Create(&image).Error)
	immichCacheFilesMu.Lock()
	done := make(chan struct{})
	go func() { gcOrphanImagesForSource(db, model.SourceImmich); close(done) }()
	select {
	case <-done:
		t.Fatal("orphan GC did not wait for cache filesystem coordination")
	case <-time.After(30 * time.Millisecond):
	}
	immichCacheFilesMu.Unlock()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("orphan GC did not resume")
	}
	require.Error(t, db.First(&model.Image{}, image.ID).Error)
}

func TestCacheImageRemovesNewFileWhenDatabasePersistenceFails(t *testing.T) {
	var jpegData bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.White)
	require.NoError(t, jpeg.Encode(&jpegData, img, nil))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(jpegData.Bytes()) }))
	defer server.Close()
	db := dateIntegrationDB(t)
	settings := NewSettingsService(db)
	require.NoError(t, settings.Set("immich_url", server.URL))
	require.NoError(t, settings.Set("immich_api_key", "key"))
	imageRow := model.Image{Source: model.SourceImmich, ExternalID: "asset"}
	require.NoError(t, db.Create(&imageRow).Error)
	bin := t.TempDir()
	script := filepath.Join(bin, "magick")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nfor last; do :; done\ncp \"$1\" \"$last\"\n"), 0o755))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	dataDir := t.TempDir()
	cache := NewImmichCacheService(db, settings, NewImmichService(db, settings), dataDir)
	require.NoError(t, db.Migrator().DropTable(&model.ImmichCache{}))
	_, err := cache.CacheImage(imageRow.ID, imageRow.ExternalID)
	require.Error(t, err)
	require.NoFileExists(t, filepath.Join(dataDir, immichCacheDirName, "asset.jpg"))
}
