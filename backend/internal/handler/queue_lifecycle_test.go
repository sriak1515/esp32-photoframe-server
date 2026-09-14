package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aitjcize/esp32-photoframe-server/backend/internal/model"
	"github.com/aitjcize/esp32-photoframe-server/backend/internal/service"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newQueueLifecycleHandler(t *testing.T) (*QueueHandler, *gorm.DB, model.Device, []model.Image) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "queue-handler.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Device{}, &model.Image{}, &model.DeviceQueueItem{}))
	device := model.Device{Name: "frame"}
	require.NoError(t, db.Create(&device).Error)
	images := []model.Image{
		{Source: model.SourceGallery, FilePath: "/private/one.jpg", Caption: "one"},
		{Source: model.SourceGallery, FilePath: "/private/two.jpg", Caption: "two"},
	}
	require.NoError(t, db.Create(&images).Error)
	queue := service.NewQueueService(db)
	return NewQueueHandler(db, queue, nil), db, device, images
}

func queueHandlerContext(method, path, body, deviceID, itemID string) (echo.Context, *httptest.ResponseRecorder) {
	e := echo.New()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("deviceId", "itemId")
	c.SetParamValues(deviceID, itemID)
	return c, rec
}

func TestQueueLifecycleMetadataResponses(t *testing.T) {
	h, db, device, images := newQueueLifecycleHandler(t)
	next := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	code, message := "upstream_unavailable", "source is temporarily unavailable"
	items := []model.DeviceQueueItem{
		{DeviceID: device.ID, ImageID: images[0].ID, Position: 10, Source: images[0].Source, State: model.QueueStateFailed, AttemptCount: 3, NextAttemptAt: &next, LastErrorCode: &code, LastError: &message},
		{DeviceID: device.ID, ImageID: images[1].ID, Position: 20, Source: images[1].Source, State: model.QueueStatePending},
	}
	require.NoError(t, db.Create(&items).Error)
	deviceID := strconv.FormatUint(uint64(device.ID), 10)

	c, rec := queueHandlerContext(http.MethodGet, "/queue", "", deviceID, "")
	require.NoError(t, h.ListQueue(c))
	require.Equal(t, http.StatusOK, rec.Code)
	var list map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	listed := list["items"].([]interface{})[0].(map[string]interface{})
	require.Equal(t, model.QueueStateFailed, listed["state"])
	require.Equal(t, float64(3), listed["attempt_count"])
	require.Equal(t, code, listed["last_error_code"])

	c, rec = queueHandlerContext(http.MethodGet, "/queue/status", "", deviceID, "")
	require.NoError(t, h.QueueStatus(c))
	var status map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &status))
	require.Equal(t, float64(images[1].ID), status["next_image_id"])
	nextItem := status["next_item"].(map[string]interface{})
	require.Equal(t, model.QueueStatePending, nextItem["state"])
	require.NotContains(t, nextItem, "image", "status must not expose internal image data")

	body := `{"image_ids":[` + strconv.FormatUint(uint64(images[0].ID), 10) + `]}`
	c, rec = queueHandlerContext(http.MethodPost, "/queue/check", body, deviceID, "")
	require.NoError(t, h.CheckQueue(c))
	var check map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &check))
	occurrences := check["occurrences"].([]interface{})
	require.Len(t, occurrences, 1)
	require.Equal(t, model.QueueStateFailed, occurrences[0].(map[string]interface{})["state"])
	require.NotContains(t, occurrences[0].(map[string]interface{}), "image")
}

func TestQueueStatusReportsActiveOwnershipBeforeLaterReadyItem(t *testing.T) {
	h, db, device, images := newQueueLifecycleHandler(t)
	claimExpiry := time.Now().Add(time.Minute)
	items := []model.DeviceQueueItem{
		{DeviceID: device.ID, ImageID: images[0].ID, Position: 10, Source: images[0].Source, State: model.QueueStatePending},
		{DeviceID: device.ID, ImageID: images[1].ID, Position: 20, Source: images[1].Source, State: model.QueueStateClaimed, ClaimExpiresAt: &claimExpiry},
	}
	require.NoError(t, db.Create(&items).Error)
	c, rec := queueHandlerContext(http.MethodGet, "/queue/status", "", strconv.FormatUint(uint64(device.ID), 10), "")
	require.NoError(t, h.QueueStatus(c))
	var status map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &status))
	require.Equal(t, float64(images[1].ID), status["next_image_id"])
	require.Equal(t, model.QueueStateClaimed, status["next_item"].(map[string]interface{})["state"])
}

func TestRetryInvalidHandler(t *testing.T) {
	h, db, device, images := newQueueLifecycleHandler(t)
	code, message := "missing_image", "image is missing"
	item := model.DeviceQueueItem{DeviceID: device.ID, ImageID: images[0].ID, Position: 10, Source: images[0].Source, State: model.QueueStatePermanentlyInvalid, AttemptCount: 2, LastErrorCode: &code, LastError: &message}
	require.NoError(t, db.Create(&item).Error)
	deviceID := strconv.FormatUint(uint64(device.ID), 10)
	itemID := strconv.FormatUint(uint64(item.ID), 10)
	c, rec := queueHandlerContext(http.MethodPost, "/queue/item/retry", "", deviceID, itemID)
	require.NoError(t, h.RetryInvalid(c))
	require.Equal(t, http.StatusNoContent, rec.Code)
	require.NoError(t, db.First(&item, item.ID).Error)
	require.Equal(t, model.QueueStatePending, item.State)
	require.Equal(t, 2, item.AttemptCount)
	require.Equal(t, message, *item.LastError)
}

func TestQueueManagementHandlerReturnsIndexedRejections(t *testing.T) {
	h, _, device, images := newQueueLifecycleHandler(t)
	deviceID := strconv.FormatUint(uint64(device.ID), 10)
	body := fmt.Sprintf(`{"image_ids":[%d,999999,%d]}`, images[0].ID, images[0].ID)
	c, rec := queueHandlerContext(http.MethodPost, "/queue", body, deviceID, "")
	require.NoError(t, h.AddToQueue(c))
	require.Equal(t, http.StatusOK, rec.Code)

	var response struct {
		Added    []model.DeviceQueueItem     `json:"added"`
		Rejected []service.QueueAddRejection `json:"rejected"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
	require.Len(t, response.Added, 2)
	require.Equal(t, images[0].ID, response.Added[0].ImageID)
	require.Equal(t, images[0].ID, response.Added[1].ImageID)
	require.Equal(t, []service.QueueAddRejection{{Index: 1, ImageID: 999999, Reason: "image_not_found"}}, response.Rejected)
}

func TestQueueEnqueueAlwaysDeduplicatesAsyncPrecacheAndRecordsDiagnostics(t *testing.T) {
	h, db, device, _ := newQueueLifecycleHandler(t)
	imageRow := model.Image{Source: model.SourceImmich, ExternalID: "asset"}
	require.NoError(t, db.Create(&imageRow).Error)
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	h.precacheImage = func(imageID uint) error {
		if imageID != imageRow.ID {
			return fmt.Errorf("unexpected pre-cache image %d", imageID)
		}
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return service.RetryableQueueFailure("upstream_unavailable", "pre-cache unavailable")
	}
	deviceID := strconv.FormatUint(uint64(device.ID), 10)
	body := fmt.Sprintf(`{"image_ids":[%d,%d]}`, imageRow.ID, imageRow.ID)
	c, _ := queueHandlerContext(http.MethodPost, "/queue", body, deviceID, "")
	require.NoError(t, h.AddToQueue(c))
	<-started
	c, _ = queueHandlerContext(http.MethodPost, "/queue", fmt.Sprintf(`{"image_ids":[%d]}`, imageRow.ID), deviceID, "")
	require.NoError(t, h.AddToQueue(c))
	require.EqualValues(t, 1, calls.Load(), "normal cache mode must not suppress or duplicate queue pre-cache")
	close(release)
	require.Eventually(t, func() bool {
		items, _, err := h.queue.List(device.ID)
		if err != nil || len(items) != 3 {
			return false
		}
		for _, item := range items {
			if item.LastErrorCode == nil || *item.LastErrorCode != "upstream_unavailable" {
				return false
			}
		}
		return true
	}, time.Second, 10*time.Millisecond)
}

func TestQueueManagementHandlerMapsConflictsTo409WithoutMutation(t *testing.T) {
	h, db, device, images := newQueueLifecycleHandler(t)
	items := []model.DeviceQueueItem{
		{DeviceID: device.ID, ImageID: images[0].ID, Position: 10, Source: images[0].Source, State: model.QueueStatePending},
		{DeviceID: device.ID, ImageID: images[1].ID, Position: 20, Source: images[1].Source, State: model.QueueStateLeased},
	}
	require.NoError(t, db.Create(&items).Error)
	deviceID := strconv.FormatUint(uint64(device.ID), 10)
	leasedID := strconv.FormatUint(uint64(items[1].ID), 10)

	c, rec := queueHandlerContext(http.MethodPut, "/queue/reorder", fmt.Sprintf(`{"item_ids":[%d,%d]}`, items[0].ID, items[1].ID), deviceID, "")
	err := h.ReorderQueue(c)
	require.Error(t, err)
	c.Echo().HTTPErrorHandler(err, c)
	require.Equal(t, http.StatusConflict, rec.Code)

	c, rec = queueHandlerContext(http.MethodDelete, "/queue/item", "", deviceID, leasedID)
	err = h.RemoveFromQueue(c)
	require.Error(t, err)
	c.Echo().HTTPErrorHandler(err, c)
	require.Equal(t, http.StatusConflict, rec.Code)

	c, rec = queueHandlerContext(http.MethodDelete, "/queue", "", deviceID, "")
	err = h.ClearQueue(c)
	require.Error(t, err)
	c.Echo().HTTPErrorHandler(err, c)
	require.Equal(t, http.StatusConflict, rec.Code)

	var count int64
	require.NoError(t, db.Model(&model.DeviceQueueItem{}).Where("device_id = ?", device.ID).Count(&count).Error)
	require.EqualValues(t, 2, count)
}
