package service

import (
	"errors"
	"fmt"

	"github.com/aitjcize/esp32-photoframe-server/backend/internal/model"
	"gorm.io/gorm"
)

const queueSoftLimit = 500

var ErrQueueItemNotFound = errors.New("queue item not found")

type QueueService struct {
	db     *gorm.DB
	immich *ImmichService
	cache  *ImmichCacheService
}

func NewQueueService(db *gorm.DB) *QueueService {
	return &QueueService{db: db}
}

func (s *QueueService) SetImmichService(immich *ImmichService, cache *ImmichCacheService) {
	s.immich, s.cache = immich, cache
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
	if s.immich != nil {
		var immichCount int64
		if err := s.db.Model(&model.Image{}).Where("id IN ? AND source = ?", imageIDs, model.SourceImmich).Count(&immichCount).Error; err != nil {
			return nil, "", err
		}
		if immichCount > 0 {
			if _, err := s.immich.DatePolicy(); err != nil {
				return nil, "", err
			}
		}
	}
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
	_, err := s.remove("device_id = ? AND id = ?", true, deviceID, itemID)
	return err
}

// RemoveByImageID removes a queue item by device and image ID.
func (s *QueueService) RemoveByImageID(deviceID uint, imageID uint) error {
	_, err := s.remove("device_id = ? AND image_id = ?", false, deviceID, imageID)
	return err
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
	return s.remove("device_id = ?", false, deviceID)
}

func (s *QueueService) remove(where string, requireMatch bool, args ...interface{}) (int64, error) {
	var policy *ImmichDatePolicy
	if s.immich != nil {
		if p, err := s.immich.DatePolicy(); err == nil {
			policy = &p
		}
	}
	immichCacheFilesMu.Lock()
	defer immichCacheFilesMu.Unlock()
	var staged []stagedCacheFile
	var removed int64
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var items []model.DeviceQueueItem
		if err := tx.Where(where, args...).Find(&items).Error; err != nil {
			return fmt.Errorf("find queue items: %w", err)
		}
		if requireMatch && len(items) == 0 {
			return ErrQueueItemNotFound
		}
		result := tx.Where(where, args...).Delete(&model.DeviceQueueItem{})
		if result.Error != nil {
			return fmt.Errorf("remove queue items: %w", result.Error)
		}
		removed = result.RowsAffected
		if policy == nil {
			return nil
		}
		seen := map[uint]bool{}
		for _, item := range items {
			if seen[item.ImageID] {
				continue
			}
			seen[item.ImageID] = true
			var image model.Image
			if err := tx.First(&image, item.ImageID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					continue
				}
				return fmt.Errorf("load queued image for cleanup: %w", err)
			}
			if image.Source != model.SourceImmich || policy.Eligible(image.PhotoTakenDate) {
				continue
			}
			var refs, memberships int64
			if err := tx.Model(&model.DeviceQueueItem{}).Where("image_id = ?", image.ID).Count(&refs).Error; err != nil {
				return err
			}
			if err := tx.Model(&model.ImageAlbumMembership{}).Where("image_id = ?", image.ID).Count(&memberships).Error; err != nil {
				return err
			}
			if refs > 0 || memberships > 0 {
				continue
			}
			var caches []model.ImmichCache
			if err := tx.Where("image_id = ?", image.ID).Find(&caches).Error; err != nil {
				return err
			}
			paths := make([]string, 0, len(caches))
			for _, cache := range caches {
				paths = append(paths, cache.FilePath)
			}
			files, err := stageCacheFiles(paths)
			if err != nil {
				return fmt.Errorf("stage cache files: %w", err)
			}
			staged = append(staged, files...)
			if len(caches) > 0 {
				if err := tx.Unscoped().Delete(&caches).Error; err != nil {
					return err
				}
			}
			if err := tx.Unscoped().Delete(&image).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		if restoreErr := restoreCacheFiles(staged); restoreErr != nil {
			return 0, fmt.Errorf("%v; restore staged cache files: %w", err, restoreErr)
		}
		return 0, err
	}
	if err := removeStagedCacheFiles(staged); err != nil {
		return removed, fmt.Errorf("remove staged cache files: %w", err)
	}
	return removed, nil
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
