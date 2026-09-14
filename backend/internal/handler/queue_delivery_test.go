package handler

import (
	"errors"
	"image"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/aitjcize/esp32-photoframe-server/backend/internal/model"
	"github.com/aitjcize/esp32-photoframe-server/backend/internal/service"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type queueDeliveryFixture struct {
	db      *gorm.DB
	now     time.Time
	queue   *service.QueueService
	handler *ImageHandler
	devices []model.Device
	images  []model.Image
}

func newQueueDeliveryFixture(t *testing.T, deviceCount, imageCount int) *queueDeliveryFixture {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "delivery.db")+"?_foreign_keys=on"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, db.AutoMigrate(&model.Device{}, &model.Image{}, &model.DeviceQueueItem{}, &model.DeviceHistory{}))

	f := &queueDeliveryFixture{db: db, now: time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)}
	for i := 0; i < deviceCount; i++ {
		device := model.Device{
			Name: "frame", Source: model.SourceGallery, Width: 8, Height: 8,
			ServerAuthoritative: true, ConfigSyncPending: true,
			DeviceConfig: "{}", DeviceProcessingSettings: "{}", DeviceColorPalette: "{}",
			ConfigLastUpdated: 10,
		}
		require.NoError(t, db.Create(&device).Error)
		f.devices = append(f.devices, device)
	}
	for i := 0; i < imageCount; i++ {
		queuedImage := model.Image{Source: model.SourceGallery, FilePath: "unused"}
		require.NoError(t, db.Create(&queuedImage).Error)
		f.images = append(f.images, queuedImage)
	}
	f.queue = f.newQueueService()
	f.handler = f.newHandler(f.queue)
	return f
}

func (f *queueDeliveryFixture) newQueueService() *service.QueueService {
	return service.NewQueueServiceWithOptions(f.db, service.QueueServiceOptions{
		Now:    func() time.Time { return f.now },
		Jitter: func(uint, int, time.Duration) time.Duration { return 0 },
	})
}

func (f *queueDeliveryFixture) newHandler(queue *service.QueueService) *ImageHandler {
	return &ImageHandler{
		db:           f.db,
		queueService: queue,
		processor:    service.NewProcessorService(),
		queueLoad: func(*model.DeviceQueueItem) (image.Image, error) {
			return image.NewRGBA(image.Rect(0, 0, 8, 8)), nil
		},
		processImage: func(image.Image, map[string]string) ([]byte, []byte, error) {
			return []byte("image"), nil, nil
		},
	}
}

func (f *queueDeliveryFixture) add(t *testing.T, device, queuedImage, position int) model.DeviceQueueItem {
	t.Helper()
	item := model.DeviceQueueItem{
		DeviceID: f.devices[device].ID, ImageID: f.images[queuedImage].ID,
		Position: position, Source: model.SourceGallery, State: model.QueueStatePending,
	}
	require.NoError(t, f.db.Create(&item).Error)
	return item
}

func imageRequest(handler *ImageHandler, deviceID uint, writer http.ResponseWriter) (int, error) {
	e := echo.New()
	if writer == nil {
		writer = httptest.NewRecorder()
	}
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/image", nil), writer)
	setDevicePrincipal(c, deviceID)
	err := handler.ServeImage(c)
	return c.Response().Status, err
}

func loadQueueItem(t *testing.T, db *gorm.DB, id uint) model.DeviceQueueItem {
	t.Helper()
	var item model.DeviceQueueItem
	require.NoError(t, db.First(&item, id).Error)
	return item
}

func TestConcurrentQueuePollBlocksLaterOccurrence(t *testing.T) {
	f := newQueueDeliveryFixture(t, 1, 2)
	first := f.add(t, 0, 0, 10)
	second := f.add(t, 0, 1, 20)
	entered := make(chan uint, 1)
	release := make(chan struct{})
	f.handler.queueLoad = func(item *model.DeviceQueueItem) (image.Image, error) {
		entered <- item.ID
		<-release
		return image.NewRGBA(image.Rect(0, 0, 8, 8)), nil
	}

	result := make(chan error, 1)
	go func() {
		_, err := imageRequest(f.handler, f.devices[0].ID, nil)
		result <- err
	}()
	require.Equal(t, first.ID, <-entered)
	status, err := imageRequest(f.handler, f.devices[0].ID, nil)
	require.Error(t, err)
	require.Equal(t, http.StatusServiceUnavailable, status)
	require.Equal(t, model.QueueStatePending, loadQueueItem(t, f.db, second.ID).State)
	close(release)
	require.NoError(t, <-result)
	require.Equal(t, model.QueueStateLeased, loadQueueItem(t, f.db, first.ID).State)
}

func TestExpiredRecoveredWorkerCannotWriteResponse(t *testing.T) {
	f := newQueueDeliveryFixture(t, 1, 2)
	first := f.add(t, 0, 0, 10)
	second := f.add(t, 0, 1, 20)
	entered := make(chan struct{})
	release := make(chan struct{})
	f.handler.queueLoad = func(item *model.DeviceQueueItem) (image.Image, error) {
		if item.ID == first.ID {
			close(entered)
			<-release
		}
		return image.NewRGBA(image.Rect(0, 0, 8, 8)), nil
	}

	firstRecorder := httptest.NewRecorder()
	firstResult := make(chan error, 1)
	go func() {
		_, err := imageRequest(f.handler, f.devices[0].ID, firstRecorder)
		firstResult <- err
	}()
	<-entered
	claimed := loadQueueItem(t, f.db, first.ID)
	f.now = claimed.ClaimExpiresAt.Add(time.Nanosecond)
	require.NoError(t, requestImageSuccessfully(f.handler, f.devices[0].ID))
	require.Equal(t, model.QueueStateLeased, loadQueueItem(t, f.db, second.ID).State)

	close(release)
	require.ErrorIs(t, <-firstResult, service.ErrQueueStaleClaim)
	require.Empty(t, firstRecorder.Body.Bytes(), "a worker that lost its claim must not offer bytes")
	require.Equal(t, model.QueueStateFailed, loadQueueItem(t, f.db, first.ID).State)
}

func TestQueueLeaseRetryReplaysOccurrenceWithoutExtendingLease(t *testing.T) {
	f := newQueueDeliveryFixture(t, 1, 2)
	first := f.add(t, 0, 0, 10)
	second := f.add(t, 0, 1, 20)
	var mu sync.Mutex
	var loaded []uint
	f.handler.queueLoad = func(item *model.DeviceQueueItem) (image.Image, error) {
		mu.Lock()
		loaded = append(loaded, item.ID)
		mu.Unlock()
		return image.NewRGBA(image.Rect(0, 0, 8, 8)), nil
	}

	status, err := imageRequest(f.handler, f.devices[0].ID, nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	originalExpiry := *loadQueueItem(t, f.db, first.ID).LeaseExpiresAt
	f.now = originalExpiry.Add(-time.Second)
	status, err = imageRequest(f.handler, f.devices[0].ID, nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	replayed := loadQueueItem(t, f.db, first.ID)
	require.Equal(t, originalExpiry, *replayed.LeaseExpiresAt)
	require.Equal(t, model.QueueStatePending, loadQueueItem(t, f.db, second.ID).State)
	require.Equal(t, []uint{first.ID, first.ID}, loaded)
}

func TestConcurrentQueueReplayBlocksLaterOccurrence(t *testing.T) {
	f := newQueueDeliveryFixture(t, 1, 2)
	first := f.add(t, 0, 0, 10)
	second := f.add(t, 0, 1, 20)
	require.NoError(t, requestImageSuccessfully(f.handler, f.devices[0].ID))
	originalExpiry := *loadQueueItem(t, f.db, first.ID).LeaseExpiresAt
	f.now = originalExpiry.Add(-time.Minute)
	entered := make(chan uint, 1)
	release := make(chan struct{})
	f.handler.queueLoad = func(item *model.DeviceQueueItem) (image.Image, error) {
		entered <- item.ID
		<-release
		return image.NewRGBA(image.Rect(0, 0, 8, 8)), nil
	}

	result := make(chan error, 1)
	go func() {
		_, err := imageRequest(f.handler, f.devices[0].ID, nil)
		result <- err
	}()
	require.Equal(t, first.ID, receiveQueueItemID(t, entered))
	status, err := imageRequest(f.handler, f.devices[0].ID, nil)
	require.Error(t, err)
	require.Equal(t, http.StatusServiceUnavailable, status)
	require.Equal(t, model.QueueStatePending, loadQueueItem(t, f.db, second.ID).State)
	close(release)
	require.NoError(t, <-result)
	require.Equal(t, originalExpiry, *loadQueueItem(t, f.db, first.ID).LeaseExpiresAt)
}

func TestFirstPostExpiryPollCompletesAndAdvances(t *testing.T) {
	f := newQueueDeliveryFixture(t, 1, 2)
	first := f.add(t, 0, 0, 10)
	second := f.add(t, 0, 1, 20)
	require.NoError(t, requestImageSuccessfully(f.handler, f.devices[0].ID))
	f.now = *loadQueueItem(t, f.db, first.ID).LeaseExpiresAt

	require.NoError(t, requestImageSuccessfully(f.handler, f.devices[0].ID))
	require.ErrorIs(t, f.db.First(&model.DeviceQueueItem{}, first.ID).Error, gorm.ErrRecordNotFound)
	require.Equal(t, model.QueueStateLeased, loadQueueItem(t, f.db, second.ID).State)
	var history []model.DeviceHistory
	require.NoError(t, f.db.Where("queue_item_id = ?", first.ID).Find(&history).Error)
	require.Len(t, history, 1)
	require.Equal(t, f.devices[0].ID, history[0].DeviceID)
	require.Equal(t, f.images[0].ID, history[0].ImageID)
}

func TestConcurrentPostExpiryPollCompletesOnceAndServesNextOnce(t *testing.T) {
	f := newQueueDeliveryFixture(t, 1, 2)
	first := f.add(t, 0, 0, 10)
	second := f.add(t, 0, 1, 20)
	require.NoError(t, requestImageSuccessfully(f.handler, f.devices[0].ID))
	f.now = *loadQueueItem(t, f.db, first.ID).LeaseExpiresAt
	entered := make(chan uint, 1)
	release := make(chan struct{})
	f.handler.queueLoad = func(item *model.DeviceQueueItem) (image.Image, error) {
		entered <- item.ID
		<-release
		return image.NewRGBA(image.Rect(0, 0, 8, 8)), nil
	}

	result := make(chan error, 1)
	go func() {
		_, err := imageRequest(f.handler, f.devices[0].ID, nil)
		result <- err
	}()
	require.Equal(t, second.ID, receiveQueueItemID(t, entered))
	status, err := imageRequest(f.handler, f.devices[0].ID, nil)
	require.Error(t, err)
	require.Equal(t, http.StatusServiceUnavailable, status)
	var histories int64
	require.NoError(t, f.db.Model(&model.DeviceHistory{}).Where("queue_item_id = ?", first.ID).Count(&histories).Error)
	require.EqualValues(t, 1, histories)
	close(release)
	require.NoError(t, <-result)
	require.Equal(t, model.QueueStateLeased, loadQueueItem(t, f.db, second.ID).State)
}

func TestQueueLeaseReplaySurvivesProcessRestart(t *testing.T) {
	f := newQueueDeliveryFixture(t, 1, 1)
	item := f.add(t, 0, 0, 10)
	require.NoError(t, requestImageSuccessfully(f.handler, f.devices[0].ID))
	expiry := *loadQueueItem(t, f.db, item.ID).LeaseExpiresAt
	f.now = expiry.Add(-time.Minute)
	restarted := f.newHandler(f.newQueueService())

	require.NoError(t, requestImageSuccessfully(restarted, f.devices[0].ID))
	got := loadQueueItem(t, f.db, item.ID)
	require.Equal(t, model.QueueStateLeased, got.State)
	require.Equal(t, expiry, *got.LeaseExpiresAt)
}

func TestQueueDeliveryFailuresBecomeRetryableAndKeepConfigPending(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*ImageHandler)
		write http.ResponseWriter
	}{
		{name: "load", setup: func(h *ImageHandler) {
			h.queueLoad = func(*model.DeviceQueueItem) (image.Image, error) { return nil, errors.New("load failed") }
		}},
		{name: "process", setup: func(h *ImageHandler) {
			h.processImage = func(image.Image, map[string]string) ([]byte, []byte, error) {
				return nil, nil, errors.New("process failed")
			}
		}},
		{name: "write", setup: func(*ImageHandler) {}, write: newFailingResponseWriter()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newQueueDeliveryFixture(t, 1, 1)
			item := f.add(t, 0, 0, 10)
			tt.setup(f.handler)
			_, err := imageRequest(f.handler, f.devices[0].ID, tt.write)
			require.Error(t, err)
			failed := loadQueueItem(t, f.db, item.ID)
			require.Equal(t, model.QueueStateFailed, failed.State)
			require.NotNil(t, failed.NextAttemptAt)
			var device model.Device
			require.NoError(t, f.db.First(&device, f.devices[0].ID).Error)
			require.True(t, device.ConfigSyncPending)
		})
	}
}

func TestDifferentDevicesDeliverIndependently(t *testing.T) {
	f := newQueueDeliveryFixture(t, 2, 2)
	items := []model.DeviceQueueItem{f.add(t, 0, 0, 10), f.add(t, 1, 1, 10)}
	entered := make(chan uint, 2)
	release := make(chan struct{})
	f.handler.queueLoad = func(item *model.DeviceQueueItem) (image.Image, error) {
		entered <- item.ID
		<-release
		return image.NewRGBA(image.Rect(0, 0, 8, 8)), nil
	}

	results := make(chan error, 2)
	for _, device := range f.devices {
		deviceID := device.ID
		go func() {
			_, err := imageRequest(f.handler, deviceID, nil)
			results <- err
		}()
	}
	seen := map[uint]bool{}
	seen[receiveQueueItemID(t, entered)] = true
	seen[receiveQueueItemID(t, entered)] = true
	require.True(t, seen[items[0].ID])
	require.True(t, seen[items[1].ID])
	close(release)
	require.NoError(t, <-results)
	require.NoError(t, <-results)
	require.Equal(t, model.QueueStateLeased, loadQueueItem(t, f.db, items[0].ID).State)
	require.Equal(t, model.QueueStateLeased, loadQueueItem(t, f.db, items[1].ID).State)
}

func requestImageSuccessfully(handler *ImageHandler, deviceID uint) error {
	status, err := imageRequest(handler, deviceID, nil)
	if err == nil && status != http.StatusOK {
		return errors.New("image request did not return HTTP 200")
	}
	return err
}

func receiveQueueItemID(t *testing.T, entered <-chan uint) uint {
	t.Helper()
	select {
	case id := <-entered:
		return id
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for queue image loading")
		return 0
	}
}

type failingResponseWriter struct {
	header http.Header
	status int
}

func newFailingResponseWriter() *failingResponseWriter {
	return &failingResponseWriter{header: make(http.Header)}
}

func (w *failingResponseWriter) Header() http.Header    { return w.header }
func (w *failingResponseWriter) WriteHeader(status int) { w.status = status }
func (w *failingResponseWriter) Write([]byte) (int, error) {
	return 0, errors.New("response write failed")
}
