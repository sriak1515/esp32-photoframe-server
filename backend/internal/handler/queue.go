package handler

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/aitjcize/esp32-photoframe-server/backend/internal/model"
	"github.com/aitjcize/esp32-photoframe-server/backend/internal/service"
	"github.com/labstack/echo/v4"
	"gorm.io/gorm"
)

type QueueHandler struct {
	db          *gorm.DB
	queue       *service.QueueService
	immichCache *service.ImmichCacheService
}

func NewQueueHandler(db *gorm.DB, queue *service.QueueService, immichCache *service.ImmichCacheService) *QueueHandler {
	return &QueueHandler{db: db, queue: queue, immichCache: immichCache}
}

func (h *QueueHandler) getDeviceID(c echo.Context) (uint, error) {
	idStr := c.Param("deviceId")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		return 0, err
	}
	return uint(id), nil
}

// ListQueue returns all items in a device's queue.
func (h *QueueHandler) ListQueue(c echo.Context) error {
	deviceID, err := h.getDeviceID(c)
	if err != nil {
		return respondError(c, http.StatusBadRequest, "invalid device id")
	}

	// Verify device exists
	var device model.Device
	if err := h.db.First(&device, deviceID).Error; err != nil {
		return respondError(c, http.StatusNotFound, "device not found")
	}

	items, count, err := h.queue.List(deviceID)
	if err != nil {
		return respondError(c, http.StatusInternalServerError, "failed to list queue")
	}

	// Build response with thumbnail URLs
	type QueueItemResponse struct {
		ID        uint      `json:"id"`
		DeviceID  uint      `json:"device_id"`
		ImageID   uint      `json:"image_id"`
		Position  int       `json:"position"`
		Source    string    `json:"source"`
		CreatedAt interface{} `json:"created_at"`
		Image     *struct {
			ID            uint   `json:"id"`
			Caption       string `json:"caption"`
			Orientation   string `json:"orientation"`
			ThumbnailURL  string `json:"thumbnail_url"`
		} `json:"image,omitempty"`
	}

	respItems := make([]QueueItemResponse, 0, len(items))
	for _, item := range items {
		qi := QueueItemResponse{
			ID:        item.ID,
			DeviceID:  item.DeviceID,
			ImageID:   item.ImageID,
			Position:  item.Position,
			Source:    item.Source,
			CreatedAt: item.CreatedAt,
		}
		if item.Image != nil {
			qi.Image = &struct {
				ID           uint   `json:"id"`
				Caption      string `json:"caption"`
				Orientation  string `json:"orientation"`
				ThumbnailURL string `json:"thumbnail_url"`
			}{
				ID:           item.Image.ID,
				Caption:      item.Image.Caption,
				Orientation:  item.Image.Orientation,
				ThumbnailURL: fmt.Sprintf("/api/gallery/thumbnail/%d", item.Image.ID),
			}
		}
		respItems = append(respItems, qi)
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"items":     respItems,
		"count":     count,
		"soft_limit": 500,
	})
}

// AddToQueue adds images to a device's queue.
func (h *QueueHandler) AddToQueue(c echo.Context) error {
	deviceID, err := h.getDeviceID(c)
	if err != nil {
		return respondError(c, http.StatusBadRequest, "invalid device id")
	}

	// Verify device exists
	var device model.Device
	if err := h.db.First(&device, deviceID).Error; err != nil {
		return respondError(c, http.StatusNotFound, "device not found")
	}

	var req struct {
		ImageIDs []uint `json:"image_ids"`
	}
	if err := c.Bind(&req); err != nil {
		return respondError(c, http.StatusBadRequest, "invalid request body")
	}

	if len(req.ImageIDs) == 0 {
		return respondError(c, http.StatusBadRequest, "image_ids is required")
	}

	items, warning, err := h.queue.Add(deviceID, req.ImageIDs)
	if err != nil {
		return respondError(c, http.StatusInternalServerError, "failed to add to queue")
	}

	// Pre-cache Immich images when cache mode is off so queued items are
	// available even if the Immich server goes offline before consumption.
	if h.immichCache != nil && !h.immichCache.Enabled() {
		for _, item := range items {
			if item.Source == model.SourceImmich {
				go h.immichCache.CacheForQueue(item.ImageID)
			}
		}
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"added":              items,
		"skipped_duplicates": []interface{}{},
		"warning":            warning,
	})
}

// RemoveFromQueue removes a single item from the queue.
func (h *QueueHandler) RemoveFromQueue(c echo.Context) error {
	deviceID, err := h.getDeviceID(c)
	if err != nil {
		return respondError(c, http.StatusBadRequest, "invalid device id")
	}

	itemIDStr := c.Param("itemId")
	itemID, err := strconv.ParseUint(itemIDStr, 10, 64)
	if err != nil {
		return respondError(c, http.StatusBadRequest, "invalid item id")
	}

	if err := h.queue.Remove(deviceID, uint(itemID)); err != nil {
		return respondError(c, http.StatusNotFound, "queue item not found")
	}

	return c.NoContent(http.StatusNoContent)
}

// ClearQueue removes all items from a device's queue.
func (h *QueueHandler) ClearQueue(c echo.Context) error {
	deviceID, err := h.getDeviceID(c)
	if err != nil {
		return respondError(c, http.StatusBadRequest, "invalid device id")
	}

	count, err := h.queue.Clear(deviceID)
	if err != nil {
		return respondError(c, http.StatusInternalServerError, "failed to clear queue")
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"deleted_count": count,
	})
}

// ReorderQueue reorders queue items.
func (h *QueueHandler) ReorderQueue(c echo.Context) error {
	deviceID, err := h.getDeviceID(c)
	if err != nil {
		return respondError(c, http.StatusBadRequest, "invalid device id")
	}

	var req struct {
		ItemIDs []uint `json:"item_ids"`
	}
	if err := c.Bind(&req); err != nil {
		return respondError(c, http.StatusBadRequest, "invalid request body")
	}

	if len(req.ItemIDs) == 0 {
		return respondError(c, http.StatusBadRequest, "item_ids is required")
	}

	if err := h.queue.Reorder(deviceID, req.ItemIDs); err != nil {
		return respondError(c, http.StatusInternalServerError, "failed to reorder queue")
	}

	return c.NoContent(http.StatusNoContent)
}

// QueueStatus returns a quick status check for the queue.
func (h *QueueHandler) QueueStatus(c echo.Context) error {
	deviceID, err := h.getDeviceID(c)
	if err != nil {
		return respondError(c, http.StatusBadRequest, "invalid device id")
	}

	count, err := h.queue.Count(deviceID)
	if err != nil {
		return respondError(c, http.StatusInternalServerError, "failed to count queue")
	}

	// Get next image ID if queue is not empty
	var nextImageID *uint
	if count > 0 {
		next, err := h.queue.GetNextForDevice(deviceID)
		if err == nil && next != nil {
			nextImageID = &next.ImageID
		}
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"count":          count,
		"soft_limit":     500,
		"next_image_id":  nextImageID,
	})
}

// CheckQueue checks if images are already in the queue.
func (h *QueueHandler) CheckQueue(c echo.Context) error {
	deviceID, err := h.getDeviceID(c)
	if err != nil {
		return respondError(c, http.StatusBadRequest, "invalid device id")
	}

	var req struct {
		ImageIDs []uint `json:"image_ids"`
	}
	if err := c.Bind(&req); err != nil {
		return respondError(c, http.StatusBadRequest, "invalid request body")
	}

	if len(req.ImageIDs) == 0 {
		return respondError(c, http.StatusBadRequest, "image_ids is required")
	}

	queued, err := h.queue.CheckQueued(deviceID, req.ImageIDs)
	if err != nil {
		return respondError(c, http.StatusInternalServerError, "failed to check queue")
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"queued": queued,
	})
}
