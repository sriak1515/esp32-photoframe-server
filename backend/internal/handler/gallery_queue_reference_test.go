package handler

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/aitjcize/esp32-photoframe-server/backend/internal/model"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestGallerySingleDeleteRetainsEveryQueuedLifecycleState(t *testing.T) {
	states := []string{model.QueueStatePending, model.QueueStateClaimed, model.QueueStateLeased, model.QueueStateFailed, model.QueueStatePermanentlyInvalid}
	for _, state := range states {
		t.Run(state, func(t *testing.T) {
			h, db, device := galleryDeleteFixture(t)
			imageRow := model.Image{Source: model.SourceGallery, FilePath: filepath.Join(t.TempDir(), "photo.jpg")}
			require.NoError(t, db.Create(&imageRow).Error)
			item := model.DeviceQueueItem{DeviceID: device.ID, ImageID: imageRow.ID, Position: 10, Source: imageRow.Source, State: state}
			require.NoError(t, db.Create(&item).Error)
			c, rec := galleryDeleteContext(http.MethodDelete, "/api/gallery/photos/1", fmt.Sprint(imageRow.ID))

			require.Error(t, h.DeletePhoto(c))
			require.Equal(t, http.StatusConflict, rec.Code)
			require.NoError(t, db.First(&model.Image{}, imageRow.ID).Error)
			require.NoError(t, db.First(&model.DeviceQueueItem{}, item.ID).Error)
		})
	}
}

func TestGalleryBulkDeleteIsAllOrNothingWithQueuedReference(t *testing.T) {
	h, db, device := galleryDeleteFixture(t)
	images := []model.Image{{Source: model.SourceGallery, FilePath: "one.jpg"}, {Source: model.SourceGallery, FilePath: "two.jpg"}}
	require.NoError(t, db.Create(&images).Error)
	require.NoError(t, db.Create(&model.DeviceQueueItem{DeviceID: device.ID, ImageID: images[0].ID, Position: 10, Source: images[0].Source, State: model.QueueStatePending}).Error)
	c, rec := galleryDeleteContext(http.MethodDelete, "/api/gallery/photos?source=gallery", "")
	c.QueryParams().Set("source", model.SourceGallery)

	require.Error(t, h.DeletePhotos(c))
	require.Equal(t, http.StatusConflict, rec.Code)
	var count int64
	require.NoError(t, db.Model(&model.Image{}).Where("id IN ?", []uint{images[0].ID, images[1].ID}).Count(&count).Error)
	require.EqualValues(t, 2, count)
}

func galleryDeleteFixture(t *testing.T) (*GalleryHandler, *gorm.DB, model.Device) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "gallery-delete.db")+"?_foreign_keys=on"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Device{}, &model.Image{}, &model.DeviceQueueItem{}))
	device := model.Device{Name: "frame"}
	require.NoError(t, db.Create(&device).Error)
	return NewGalleryHandler(db, nil, nil, t.TempDir()), db, device
}

func galleryDeleteContext(method, path, id string) (echo.Context, *httptest.ResponseRecorder) {
	e := echo.New()
	rec := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(method, path, nil), rec)
	c.SetParamNames("id")
	c.SetParamValues(id)
	return c, rec
}
