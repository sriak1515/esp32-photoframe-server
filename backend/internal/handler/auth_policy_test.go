package handler

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/aitjcize/esp32-photoframe-server/backend/internal/middleware"
	"github.com/aitjcize/esp32-photoframe-server/backend/internal/model"
	"github.com/aitjcize/esp32-photoframe-server/backend/internal/service"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestQueueManagementRequiresAdministratorBeforeDeviceLookup(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "queue-auth.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.User{}, &model.APIKey{}, &model.UserSession{},
		&model.Device{}, &model.Image{}, &model.DeviceQueueItem{},
	))
	auth := service.NewAuthService(db, "test-secret")
	require.NoError(t, auth.Register("admin", "password"))
	adminToken, err := auth.Login("admin", "password", "test", "127.0.0.1")
	require.NoError(t, err)
	adminClaims, err := auth.ValidateToken(adminToken)
	require.NoError(t, err)

	device := model.Device{Name: "frame"}
	require.NoError(t, db.Create(&device).Error)
	deviceToken, err := auth.GenerateDeviceToken(adminClaims.UserID, adminClaims.Username, device.Name, &device.ID)
	require.NoError(t, err)

	queueHandler := NewQueueHandler(db, service.NewQueueService(db), nil)
	e := echo.New()
	authMiddleware := middleware.JWTMiddleware(auth)
	e.GET("/api/devices/:deviceId/queue", queueHandler.ListQueue, authMiddleware, middleware.RequireAdministrator)
	e.POST("/api/devices/:deviceId/queue", queueHandler.AddToQueue, authMiddleware, middleware.RequireAdministrator)
	e.DELETE("/api/devices/:deviceId/queue/:itemId", queueHandler.RemoveFromQueue, authMiddleware, middleware.RequireAdministrator)
	e.DELETE("/api/devices/:deviceId/queue", queueHandler.ClearQueue, authMiddleware, middleware.RequireAdministrator)
	e.PUT("/api/devices/:deviceId/queue/reorder", queueHandler.ReorderQueue, authMiddleware, middleware.RequireAdministrator)
	e.GET("/api/devices/:deviceId/queue/status", queueHandler.QueueStatus, authMiddleware, middleware.RequireAdministrator)
	e.POST("/api/devices/:deviceId/queue/check", queueHandler.CheckQueue, authMiddleware, middleware.RequireAdministrator)
	e.POST("/api/devices/:deviceId/queue/:itemId/retry", queueHandler.RetryInvalid, authMiddleware, middleware.RequireAdministrator)

	request := func(method, path, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set(echo.HeaderAuthorization, "Bearer "+token)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec
	}

	basePath := "/api/devices/" + fmt.Sprint(device.ID) + "/queue"
	require.Equal(t, http.StatusOK, request(http.MethodGet, basePath, adminToken).Code)
	for _, route := range []struct {
		method, suffix string
	}{
		{http.MethodGet, ""},
		{http.MethodPost, ""},
		{http.MethodDelete, "/1"},
		{http.MethodDelete, ""},
		{http.MethodPut, "/reorder"},
		{http.MethodGet, "/status"},
		{http.MethodPost, "/check"},
		{http.MethodPost, "/1/retry"},
	} {
		require.Equal(t, http.StatusForbidden, request(route.method, basePath+route.suffix, deviceToken).Code)
	}

	existing := request(http.MethodGet, basePath, deviceToken)
	missing := request(http.MethodGet, "/api/devices/999999/queue", deviceToken)
	require.Equal(t, http.StatusForbidden, existing.Code)
	require.Equal(t, http.StatusForbidden, missing.Code)
	require.JSONEq(t, existing.Body.String(), missing.Body.String(),
		"authorization must not reveal whether the target device exists")
}

func TestImageDeliveryRejectsUnboundPrincipalBeforeDatabaseAccess(t *testing.T) {
	e := echo.New()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/image", nil), httptest.NewRecorder())
	middleware.SetPrincipal(c, middleware.AuthenticatedPrincipal{Type: middleware.PrincipalDevice})

	h := &ImageHandler{}
	require.EqualError(t, h.ServeImage(c), "bound device token required")
	require.Equal(t, http.StatusForbidden, c.Response().Status)
}

func TestImageDeliveryIgnoresCrossDeviceOverrides(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "image-auth.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Device{}))
	bound := model.Device{Name: "bound"}
	other := model.Device{Name: "other"}
	require.NoError(t, db.Create(&bound).Error)
	require.NoError(t, db.Create(&other).Error)

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/image/immich?device_id=%d", other.ID), nil)
	req.Header.Set("X-Hostname", other.Name)
	req.Header.Set("X-Battery-Percentage", "73")
	c := e.NewContext(req, httptest.NewRecorder())
	setDevicePrincipal(c, bound.ID)

	h := &ImageHandler{db: db}
	require.EqualError(t, h.ServeImage(c), "no image source configured for this device — set the device's Image Source in the server")
	require.Equal(t, http.StatusBadRequest, c.Response().Status)

	var gotBound, gotOther model.Device
	require.NoError(t, db.First(&gotBound, bound.ID).Error)
	require.NoError(t, db.First(&gotOther, other.ID).Error)
	require.Equal(t, 73, gotBound.BatteryLevel)
	require.Equal(t, -1, gotOther.BatteryLevel)
}
