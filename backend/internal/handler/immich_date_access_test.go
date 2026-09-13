package handler

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aitjcize/esp32-photoframe-server/backend/internal/model"
	"github.com/aitjcize/esp32-photoframe-server/backend/internal/service"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func accessTestDB(t *testing.T) (*gorm.DB, *service.SettingsService, *service.ImmichService) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Setting{}, &model.Image{}, &model.DeviceQueueItem{}, &model.ImmichCache{}))
	settings := service.NewSettingsService(db)
	return db, settings, service.NewImmichService(db, settings)
}

func handleEchoError(e *echo.Echo, c echo.Context, err error) {
	if err != nil {
		e.HTTPErrorHandler(err, c)
	}
}

func TestExplicitNonImmichGalleryWorksWithInvalidImmichPolicy(t *testing.T) {
	db, settings, immich := accessTestDB(t)
	require.NoError(t, settings.Set("immich_date_from", "bad"))
	require.NoError(t, db.Create(&model.Image{Source: model.SourceGallery, CreatedAt: time.Now()}).Error)
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/?source=gallery", nil)
	rec := httptest.NewRecorder()
	ctx := e.NewContext(req, rec)
	handleEchoError(e, ctx, NewGalleryHandler(db, nil, immich, t.TempDir()).ListPhotos(ctx))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"total":1`)
}

func TestQueuedImmichThumbnailWorksWithInvalidPolicy(t *testing.T) {
	db, settings, immich := accessTestDB(t)
	require.NoError(t, settings.Set("immich_date_from", "bad"))
	image := model.Image{Source: model.SourceImmich, ExternalID: "x"}
	require.NoError(t, db.Create(&image).Error)
	require.NoError(t, db.Create(&model.DeviceQueueItem{DeviceID: 1, ImageID: image.ID, Position: 10, Source: model.SourceImmich}).Error)
	path := filepath.Join(t.TempDir(), "x.jpg")
	require.NoError(t, os.WriteFile(path, []byte("image"), 0600))
	require.NoError(t, db.Create(&model.ImmichCache{ImageID: image.ID, AssetID: "x", FilePath: path, CachedAt: time.Now()}).Error)
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	ctx := e.NewContext(req, rec)
	ctx.SetPath("/:id")
	ctx.SetParamNames("id")
	ctx.SetParamValues("1")
	handleEchoError(e, ctx, NewGalleryHandler(db, nil, immich, t.TempDir()).GetThumbnail(ctx))
	require.Equal(t, http.StatusOK, rec.Code)
}

func TestPushToDeviceEnforcesImmichPolicy(t *testing.T) {
	db, settings, immich := accessTestDB(t)
	require.NoError(t, settings.SetImmichDatePair("2024-02-01", ""))
	date := "2024-01-01"
	image := model.Image{Source: model.SourceImmich, ExternalID: "x", PhotoTakenDate: &date}
	require.NoError(t, db.Create(&image).Error)
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"image_id":1}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	ctx := e.NewContext(req, rec)
	ctx.SetPath("/:id")
	ctx.SetParamNames("id")
	ctx.SetParamValues("1")
	handleEchoError(e, ctx, NewDeviceHandler(nil, nil, immich, nil, db).PushToDevice(ctx))
	require.Equal(t, http.StatusBadRequest, rec.Code)
}
