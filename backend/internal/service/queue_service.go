package service

import (
	"fmt"

	"github.com/aitjcize/esp32-photoframe-server/backend/internal/model"
	"gorm.io/gorm"
)

const queueSoftLimit = 500

type QueueService struct {
	db *gorm.DB
}

func NewQueueService(db *gorm.DB) *QueueService {
	return &QueueService{db: db}
}

// GetNextForDevice returns the next queue item for the device (lowest position),
// or nil if the queue is empty.
func (s *QueueService) GetNextForDevice(deviceID uint) (*model.DeviceQueueItem, error) {
	var item model.DeviceQueueItem
	err := s.db.
		Preload("Image").
		Where("device_id = ?", deviceID).
		Order("position ASC").
		First(&item).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("get next queue item: %w", err)
	}
	return &item, nil
}

// Add adds images to the device's queue. Returns created items and a warning
// if the soft limit is reached.
func (s *QueueService) Add(deviceID uint, imageIDs []uint) ([]model.DeviceQueueItem, string, error) {
	var items []model.DeviceQueueItem
	var warning string

	// Get current max position
	var maxPosition int
	err := s.db.Model(&model.DeviceQueueItem{}).
		Where("device_id = ?", deviceID).
		Select("COALESCE(MAX(position), 0)").
		Scan(&maxPosition).Error
	if err != nil {
		return nil, "", fmt.Errorf("get max position: %w", err)
	}

	// Check current count for soft limit warning
	var count int64
	err = s.db.Model(&model.DeviceQueueItem{}).
		Where("device_id = ?", deviceID).
		Count(&count).Error
	if err != nil {
		return nil, "", fmt.Errorf("count queue items: %w", err)
	}

	for i, imageID := range imageIDs {
		// Verify image exists
		var image model.Image
		if err := s.db.First(&image, imageID).Error; err != nil {
			continue // skip non-existent images
		}

		// Check for duplicate
		var exists int64
		err := s.db.Model(&model.DeviceQueueItem{}).
			Where("device_id = ? AND image_id = ?", deviceID, imageID).
			Count(&exists).Error
		if err != nil {
			return nil, "", fmt.Errorf("check duplicate: %w", err)
		}
		if exists > 0 {
			continue // skip duplicates
		}

		item := model.DeviceQueueItem{
			DeviceID: deviceID,
			ImageID:  imageID,
			Position: maxPosition + (i+1)*10,
			Source:   image.Source,
		}
		if err := s.db.Create(&item).Error; err != nil {
			return nil, "", fmt.Errorf("create queue item: %w", err)
		}
		items = append(items, item)
	}

	if count+int64(len(items)) >= queueSoftLimit {
		warning = fmt.Sprintf("Queue has reached soft limit of %d items", queueSoftLimit)
	}

	return items, warning, nil
}

// Remove deletes a single queue item by ID, verifying device ownership.
func (s *QueueService) Remove(deviceID uint, itemID uint) error {
	result := s.db.
		Where("device_id = ? AND id = ?", deviceID, itemID).
		Delete(&model.DeviceQueueItem{})
	if result.Error != nil {
		return fmt.Errorf("remove queue item: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("queue item not found")
	}
	return nil
}

// RemoveByImageID removes a queue item by device and image ID.
func (s *QueueService) RemoveByImageID(deviceID uint, imageID uint) error {
	result := s.db.
		Where("device_id = ? AND image_id = ?", deviceID, imageID).
		Delete(&model.DeviceQueueItem{})
	if result.Error != nil {
		return fmt.Errorf("remove queue item by image: %w", result.Error)
	}
	return nil
}

// Reorder reorders queue items. itemIDs is the new order (first = lowest position).
func (s *QueueService) Reorder(deviceID uint, itemIDs []uint) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		// First pass: move all items to temporary negative positions to avoid UNIQUE conflicts
		for i, itemID := range itemIDs {
			tempPos := -(len(itemIDs) - i)
			if err := tx.Model(&model.DeviceQueueItem{}).
				Where("device_id = ? AND id = ?", deviceID, itemID).
				Update("position", tempPos).Error; err != nil {
				return fmt.Errorf("reorder item %d: %w", itemID, err)
			}
		}
		// Second pass: assign final positive positions
		for i, itemID := range itemIDs {
			position := (i + 1) * 10
			if err := tx.Model(&model.DeviceQueueItem{}).
				Where("device_id = ? AND id = ?", deviceID, itemID).
				Update("position", position).Error; err != nil {
				return fmt.Errorf("reorder item %d: %w", itemID, err)
			}
		}
		return nil
	})
}

// List returns all queue items for a device with image metadata, ordered by position.
func (s *QueueService) List(deviceID uint) ([]model.DeviceQueueItem, int64, error) {
	var items []model.DeviceQueueItem
	var count int64

	err := s.db.Model(&model.DeviceQueueItem{}).
		Where("device_id = ?", deviceID).
		Count(&count).Error
	if err != nil {
		return nil, 0, fmt.Errorf("count queue items: %w", err)
	}

	err = s.db.
		Preload("Image").
		Where("device_id = ?", deviceID).
		Order("position ASC").
		Find(&items).Error
	if err != nil {
		return nil, 0, fmt.Errorf("list queue items: %w", err)
	}

	return items, count, nil
}

// Clear removes all queue items for a device. Returns the count of deleted items.
func (s *QueueService) Clear(deviceID uint) (int64, error) {
	result := s.db.
		Where("device_id = ?", deviceID).
		Delete(&model.DeviceQueueItem{})
	if result.Error != nil {
		return 0, fmt.Errorf("clear queue: %w", result.Error)
	}
	return result.RowsAffected, nil
}

// Count returns the number of items in the device's queue.
func (s *QueueService) Count(deviceID uint) (int64, error) {
	var count int64
	err := s.db.Model(&model.DeviceQueueItem{}).
		Where("device_id = ?", deviceID).
		Count(&count).Error
	if err != nil {
		return 0, fmt.Errorf("count queue items: %w", err)
	}
	return count, nil
}

// IsInQueue checks if an image is already in the device's queue.
func (s *QueueService) IsInQueue(deviceID uint, imageID uint) (bool, error) {
	var count int64
	err := s.db.Model(&model.DeviceQueueItem{}).
		Where("device_id = ? AND image_id = ?", deviceID, imageID).
		Count(&count).Error
	if err != nil {
		return false, fmt.Errorf("check queue membership: %w", err)
	}
	return count > 0, nil
}

// CheckQueued returns the subset of imageIDs that are already in the device's queue.
func (s *QueueService) CheckQueued(deviceID uint, imageIDs []uint) ([]uint, error) {
	var queued []uint
	err := s.db.Model(&model.DeviceQueueItem{}).
		Where("device_id = ? AND image_id IN ?", deviceID, imageIDs).
		Pluck("image_id", &queued).Error
	if err != nil {
		return nil, fmt.Errorf("check queued images: %w", err)
	}
	return queued, nil
}
