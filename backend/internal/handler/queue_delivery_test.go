package handler

import (
	"testing"

	"github.com/aitjcize/esp32-photoframe-server/backend/internal/model"
	"github.com/aitjcize/esp32-photoframe-server/backend/internal/service"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestSuccessfulQueueDeliveryConsumesWhenHistoryWriteFails(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:queue-delivery?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	// DeviceHistory is intentionally not migrated, forcing the ancillary write to fail.
	require.NoError(t, db.AutoMigrate(&model.Image{}, &model.DeviceQueueItem{}))
	image := model.Image{Source: model.SourceGallery}
	require.NoError(t, db.Create(&image).Error)
	item := model.DeviceQueueItem{DeviceID: 1, ImageID: image.ID, Position: 10, Source: image.Source}
	require.NoError(t, db.Create(&item).Error)
	queue := service.NewQueueService(db)
	h := &ImageHandler{db: db, queueService: queue}
	h.completeQueuedDelivery(1, item.ID, image.ID)
	count, err := queue.Count(1)
	require.NoError(t, err)
	require.Zero(t, count)
}
