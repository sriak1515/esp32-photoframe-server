package handler

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/aitjcize/esp32-photoframe-server/backend/internal/model"
	"github.com/aitjcize/esp32-photoframe-server/backend/internal/service"
	"github.com/labstack/echo/v4"
	"gorm.io/gorm"
)

type QueueHandler struct {
	db            *gorm.DB
	queue         *service.QueueService
	immichCache   *service.ImmichCacheService
	precacheMu    sync.Mutex
	precaching    map[uint][]uint
	precacheImage func(uint) error
}

type queueLifecycleResponse struct {
	ID             uint       `json:"id"`
	ImageID        uint       `json:"image_id"`
	Position       int        `json:"position"`
	Source         string     `json:"source"`
	State          string     `json:"state"`
	ClaimExpiresAt *time.Time `json:"claim_expires_at,omitempty"`
	LeaseExpiresAt *time.Time `json:"lease_expires_at,omitempty"`
	AttemptCount   int        `json:"attempt_count"`
	NextAttemptAt  *time.Time `json:"next_attempt_at,omitempty"`
	LastAttemptAt  *time.Time `json:"last_attempt_at,omitempty"`
	LastErrorCode  *string    `json:"last_error_code,omitempty"`
	LastError      *string    `json:"last_error,omitempty"`
}

func queueLifecycle(item *model.DeviceQueueItem) queueLifecycleResponse {
	return queueLifecycleResponse{
		ID: item.ID, ImageID: item.ImageID, Position: item.Position, Source: item.Source, State: item.State,
		ClaimExpiresAt: item.ClaimExpiresAt, LeaseExpiresAt: item.LeaseExpiresAt,
		AttemptCount: item.AttemptCount, NextAttemptAt: item.NextAttemptAt, LastAttemptAt: item.LastAttemptAt,
		LastErrorCode: item.LastErrorCode, LastError: item.LastError,
	}
}

func NewQueueHandler(db *gorm.DB, queue *service.QueueService, immichCache *service.ImmichCacheService) *QueueHandler {
	h := &QueueHandler{db: db, queue: queue, immichCache: immichCache, precaching: make(map[uint][]uint)}
	if immichCache != nil {
		h.precacheImage = immichCache.CacheForQueue
	}
	return h
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
		ID             uint        `json:"id"`
		DeviceID       uint        `json:"device_id"`
		ImageID        uint        `json:"image_id"`
		Position       int         `json:"position"`
		Source         string      `json:"source"`
		CreatedAt      interface{} `json:"created_at"`
		State          string      `json:"state"`
		ClaimExpiresAt *time.Time  `json:"claim_expires_at,omitempty"`
		LeaseExpiresAt *time.Time  `json:"lease_expires_at,omitempty"`
		AttemptCount   int         `json:"attempt_count"`
		NextAttemptAt  *time.Time  `json:"next_attempt_at,omitempty"`
		LastAttemptAt  *time.Time  `json:"last_attempt_at,omitempty"`
		LastErrorCode  *string     `json:"last_error_code,omitempty"`
		LastError      *string     `json:"last_error,omitempty"`
		CacheStatus    string      `json:"cache_status,omitempty"`
		Image          *struct {
			ID           uint   `json:"id"`
			Caption      string `json:"caption"`
			Orientation  string `json:"orientation"`
			ThumbnailURL string `json:"thumbnail_url"`
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
			State:     item.State, ClaimExpiresAt: item.ClaimExpiresAt, LeaseExpiresAt: item.LeaseExpiresAt,
			AttemptCount: item.AttemptCount, NextAttemptAt: item.NextAttemptAt, LastAttemptAt: item.LastAttemptAt,
			LastErrorCode: item.LastErrorCode, LastError: item.LastError,
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
		if item.Source == model.SourceImmich && h.immichCache != nil {
			qi.CacheStatus = h.immichCache.QueueCacheStatus(item.ImageID)
		}
		respItems = append(respItems, qi)
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"items":      respItems,
		"count":      count,
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

	items, rejected, warning, err := h.queue.Add(deviceID, req.ImageIDs)
	if err != nil {
		var configErr *service.ImmichDatePolicyError
		if errors.As(err, &configErr) {
			return respondError(c, http.StatusBadRequest, err.Error())
		}
		return respondError(c, http.StatusInternalServerError, "failed to add to queue")
	}

	h.precache(items)

	return c.JSON(http.StatusOK, map[string]interface{}{
		"added":    items,
		"rejected": rejected,
		"warning":  warning,
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
		if errors.Is(err, service.ErrQueueItemNotFound) {
			return respondError(c, http.StatusNotFound, err.Error())
		}
		if errors.Is(err, service.ErrQueueConflict) {
			return respondError(c, http.StatusConflict, err.Error())
		}
		return respondError(c, http.StatusInternalServerError, err.Error())
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
		if errors.Is(err, service.ErrQueueConflict) {
			return respondError(c, http.StatusConflict, err.Error())
		}
		return respondError(c, http.StatusInternalServerError, err.Error())
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

	if req.ItemIDs == nil {
		return respondError(c, http.StatusBadRequest, "item_ids is required")
	}

	if err := h.queue.Reorder(deviceID, req.ItemIDs); err != nil {
		if errors.Is(err, service.ErrQueueConflict) {
			return respondError(c, http.StatusConflict, err.Error())
		}
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

	items, _, err := h.queue.List(deviceID)
	if err != nil {
		return respondError(c, http.StatusInternalServerError, "failed to read queue status")
	}
	var nextImageID *uint
	var nextItem *queueLifecycleResponse
	var readyItem *queueLifecycleResponse
	var readyImageID *uint
	stateCounts := map[string]int{}
	now := time.Now()
	for i := range items {
		item := &items[i]
		stateCounts[item.State]++
		if nextItem == nil && (item.State == model.QueueStateClaimed || item.State == model.QueueStateLeased) {
			lifecycle := queueLifecycle(item)
			nextItem, nextImageID = &lifecycle, &item.ImageID
		} else if readyItem == nil && (item.State == model.QueueStatePending || (item.State == model.QueueStateFailed && (item.NextAttemptAt == nil || !item.NextAttemptAt.After(now)))) {
			lifecycle := queueLifecycle(item)
			readyItem, readyImageID = &lifecycle, &item.ImageID
		}
	}
	if nextItem == nil {
		nextItem, nextImageID = readyItem, readyImageID
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"count":         count,
		"soft_limit":    500,
		"next_image_id": nextImageID,
		"next_item":     nextItem,
		"state_counts":  stateCounts,
	})
}

func (h *QueueHandler) precache(items []model.DeviceQueueItem) {
	if h.precacheImage == nil {
		return
	}
	for _, item := range items {
		if item.Source != model.SourceImmich {
			continue
		}
		h.precacheMu.Lock()
		waiting, running := h.precaching[item.ImageID]
		h.precaching[item.ImageID] = append(waiting, item.ID)
		h.precacheMu.Unlock()
		if running {
			continue
		}
		go func(imageID uint) {
			err := h.precacheImage(imageID)
			h.precacheMu.Lock()
			itemIDs := h.precaching[imageID]
			delete(h.precaching, imageID)
			h.precacheMu.Unlock()
			for _, itemID := range itemIDs {
				h.queue.RecordPrecacheResult(itemID, err)
			}
		}(item.ImageID)
	}
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

	items, _, err := h.queue.List(deviceID)
	if err != nil {
		return respondError(c, http.StatusInternalServerError, "failed to check queue")
	}
	requested := make(map[uint]bool, len(req.ImageIDs))
	for _, id := range req.ImageIDs {
		requested[id] = true
	}
	occurrences := make([]queueLifecycleResponse, 0)
	for _, item := range items {
		if requested[item.ImageID] && item.State != model.QueueStateDelivered {
			occurrences = append(occurrences, queueLifecycle(&item))
		}
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"queued": queued, "occurrences": occurrences})
}

func (h *QueueHandler) RetryInvalid(c echo.Context) error {
	deviceID, err := h.getDeviceID(c)
	if err != nil {
		return respondError(c, http.StatusBadRequest, "invalid device id")
	}
	itemID, err := strconv.ParseUint(c.Param("itemId"), 10, 64)
	if err != nil {
		return respondError(c, http.StatusBadRequest, "invalid item id")
	}
	if err := h.queue.RetryInvalid(deviceID, uint(itemID)); err != nil {
		if errors.Is(err, service.ErrQueueItemNotFound) {
			return respondError(c, http.StatusNotFound, err.Error())
		}
		return respondError(c, http.StatusInternalServerError, err.Error())
	}
	return c.NoContent(http.StatusNoContent)
}
