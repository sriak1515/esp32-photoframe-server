package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/aitjcize/esp32-photoframe-server/backend/internal/model"
	"github.com/labstack/echo/v4"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func configTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Device{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestAuthoritativeSyncIgnoresNewerDeviceState(t *testing.T) {
	db := configTestDB(t)
	device := model.Device{DeviceConfig: `{"owner":"server"}`, DeviceProcessingSettings: `{"converter":"server"}`, DeviceColorPalette: `{"owner":"server"}`, ConfigLastUpdated: 10, ServerAuthoritative: true}
	if err := db.Create(&device).Error; err != nil {
		t.Fatal(err)
	}
	h := &ImageHandler{db: db}
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/device-config/sync", strings.NewReader(`{"config":{"owner":"device"},"processing_settings":{"converter":"device"},"color_palette":{"owner":"device"},"config_last_updated":20}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set("device_id", device.ID)
	if err := h.SyncDeviceConfig(c); err != nil {
		t.Fatal(err)
	}
	var got model.Device
	if err := db.First(&got, device.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.DeviceConfig != device.DeviceConfig || got.DeviceProcessingSettings != device.DeviceProcessingSettings || got.DeviceColorPalette != device.DeviceColorPalette || got.ConfigLastUpdated != 10 {
		t.Fatalf("authoritative state overwritten: %+v", got)
	}
}

func TestNonAuthoritativeSyncPreservesServerOnlyProcessingKeys(t *testing.T) {
	db := configTestDB(t)
	device := model.Device{DeviceProcessingSettings: `{"converter":"server","autoMode":true,"exposure":1}`, ConfigLastUpdated: 10, ServerAuthoritative: false}
	if err := db.Create(&device).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&device).Update("server_authoritative", false).Error; err != nil {
		t.Fatal(err)
	}
	h := &ImageHandler{db: db}
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/device-config/sync", strings.NewReader(`{"processing_settings":{"exposure":2},"config_last_updated":20}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	c := e.NewContext(req, httptest.NewRecorder())
	c.Set("device_id", device.ID)
	if err := h.SyncDeviceConfig(c); err != nil {
		t.Fatal(err)
	}
	var got model.Device
	if err := db.First(&got, device.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.DeviceProcessingSettings, `"converter":"server"`) || !strings.Contains(got.DeviceProcessingSettings, `"exposure":2`) {
		t.Fatalf("processing merge = %s", got.DeviceProcessingSettings)
	}
}

func TestAuthoritativeDeferredPayloadClearsMatchingPendingGeneration(t *testing.T) {
	db := configTestDB(t)
	device := model.Device{DeviceConfig: `{"owner":"server"}`, ConfigLastUpdated: 10, ServerAuthoritative: true, ConfigSyncPending: true}
	if err := db.Create(&device).Error; err != nil {
		t.Fatal(err)
	}
	h := &ImageHandler{db: db}
	e := echo.New()
	rec := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/image", nil), rec)
	delivery := h.applyConfigSync(c, &device, true, true)
	if rec.Header().Get("X-Config-Payload") == "" {
		t.Fatal("deferred payload missing")
	}
	h.completeConfigDelivery(delivery, nil)
	var got model.Device
	if err := db.First(&got, device.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.ConfigSyncPending {
		t.Fatal("matching generation remained pending after response write")
	}
}

func TestAuthoritativeDeferredPayloadDoesNotClearNewerGeneration(t *testing.T) {
	db := configTestDB(t)
	device := model.Device{DeviceConfig: `{"owner":"old"}`, ConfigLastUpdated: 10, ServerAuthoritative: true, ConfigSyncPending: true}
	if err := db.Create(&device).Error; err != nil {
		t.Fatal(err)
	}
	h := &ImageHandler{db: db}
	e := echo.New()
	rec := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/image", nil), rec)
	delivery := h.applyConfigSync(c, &device, true, true)
	if err := db.Model(&model.Device{}).Where("id = ?", device.ID).Updates(map[string]interface{}{"config_last_updated": 11, "config_sync_pending": true}).Error; err != nil {
		t.Fatal(err)
	}
	h.completeConfigDelivery(delivery, nil)
	var got model.Device
	if err := db.First(&got, device.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !got.ConfigSyncPending {
		t.Fatal("stale response cleared newer generation")
	}
}

func TestDeferredResponseErrorRetainsPending(t *testing.T) {
	db := configTestDB(t)
	device := model.Device{DeviceConfig: `{}`, DeviceProcessingSettings: `{}`, DeviceColorPalette: `{}`, ConfigLastUpdated: 10, ServerAuthoritative: true, ConfigSyncPending: true}
	if err := db.Create(&device).Error; err != nil {
		t.Fatal(err)
	}
	h := &ImageHandler{db: db}
	h.completeConfigDelivery(&configDelivery{deviceID: device.ID, generation: 10}, errors.New("write failed"))
	var got model.Device
	if err := db.First(&got, device.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !got.ConfigSyncPending {
		t.Fatal("failed response cleared pending")
	}
}

func TestAuthoritativeDirectDeliveryOrderAndPartialFailure(t *testing.T) {
	for _, test := range []struct {
		name, failPath string
		wantPending    bool
	}{
		{name: "success", wantPending: false},
		{name: "processing failure", failPath: "/api/settings/processing", wantPending: true},
		{name: "palette failure", failPath: "/api/settings/palette", wantPending: true},
		{name: "config failure", failPath: "/api/config", wantPending: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var mu sync.Mutex
			paths := []string{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				paths = append(paths, r.URL.Path)
				mu.Unlock()
				if r.URL.Path == test.failPath {
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()
			db := configTestDB(t)
			device := model.Device{Host: strings.TrimPrefix(server.URL, "http://"), DeviceConfig: `{}`, DeviceProcessingSettings: `{}`, DeviceColorPalette: `{}`, ConfigLastUpdated: 10, ServerAuthoritative: true, ConfigSyncPending: true}
			if err := db.Create(&device).Error; err != nil {
				t.Fatal(err)
			}
			h := &ImageHandler{db: db}
			e := echo.New()
			req := httptest.NewRequest(http.MethodPut, "/api/devices/1/config", strings.NewReader(`{"config":{"image_url":"https://example/image"},"processing_settings":{"exposure":1},"color_palette":{"black":{"r":0}}}`))
			req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
			c := e.NewContext(req, httptest.NewRecorder())
			c.SetPath("/api/devices/:id/config")
			c.SetParamNames("id")
			c.SetParamValues(fmt.Sprint(device.ID))
			if err := h.UpdateDeviceConfig(c); err != nil {
				t.Fatal(err)
			}
			var body map[string]interface{}
			if err := json.Unmarshal(c.Response().Writer.(*httptest.ResponseRecorder).Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body["config_sync_pending"] != test.wantPending {
				t.Fatalf("pending=%v want %v", body["config_sync_pending"], test.wantPending)
			}
			want := []string{"/api/settings/processing", "/api/settings/palette", "/api/config"}
			if strings.Join(paths, ",") != strings.Join(want, ",") {
				t.Fatalf("calls=%v want %v", paths, want)
			}
		})
	}
}

func TestPersistenceFailurePreventsDirectDelivery(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	db := configTestDB(t)
	device := model.Device{Host: strings.TrimPrefix(server.URL, "http://"), DeviceConfig: `{}`, DeviceProcessingSettings: `{}`, DeviceColorPalette: `{}`, ServerAuthoritative: true}
	if err := db.Create(&device).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Callback().Update().Before("gorm:update").Register("test:fail-device-save", func(tx *gorm.DB) {
		if tx.Statement.Table == "devices" {
			tx.AddError(errors.New("injected persistence failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	h := &ImageHandler{db: db}
	e := echo.New()
	req := httptest.NewRequest(http.MethodPut, "/", strings.NewReader(`{"config":{},"processing_settings":{},"color_palette":{}}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues(fmt.Sprint(device.ID))
	if err := h.UpdateDeviceConfig(c); err != nil {
		e.HTTPErrorHandler(err, c)
	}
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500", rec.Code)
	}
	if calls != 0 {
		t.Fatalf("device received %d calls before persistence", calls)
	}
}
