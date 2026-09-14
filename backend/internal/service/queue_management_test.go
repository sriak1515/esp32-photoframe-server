package service

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/aitjcize/esp32-photoframe-server/backend/internal/model"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type queueManagementFixture struct {
	db      *gorm.DB
	service *QueueService
	device  model.Device
	images  []model.Image
}

func newQueueManagementFixture(t *testing.T) *queueManagementFixture {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "queue-management.db") + "?_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=on"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Device{}, &model.Image{}, &model.DeviceQueueItem{}))
	require.NoError(t, db.Exec("CREATE UNIQUE INDEX queue_positions_unique ON device_image_queue(device_id, position)").Error)
	device := model.Device{Name: "frame"}
	require.NoError(t, db.Create(&device).Error)
	images := make([]model.Image, 4)
	for i := range images {
		images[i] = model.Image{Source: model.SourceGallery, FilePath: fmt.Sprintf("image-%d.jpg", i)}
		require.NoError(t, db.Create(&images[i]).Error)
	}
	return &queueManagementFixture{db: db, service: NewQueueService(db), device: device, images: images}
}

func (f *queueManagementFixture) createItem(t *testing.T, image int, position int, state string) model.DeviceQueueItem {
	t.Helper()
	item := model.DeviceQueueItem{
		DeviceID: f.device.ID, ImageID: f.images[image].ID, Position: position,
		Source: f.images[image].Source, State: state,
	}
	require.NoError(t, f.db.Create(&item).Error)
	return item
}

func TestQueueAddPreservesDuplicatesAndClassifiesEveryOccurrence(t *testing.T) {
	f := newQueueManagementFixture(t)
	unsupported := model.Image{Source: model.SourceFractal}
	require.NoError(t, f.db.Create(&unsupported).Error)

	items, rejected, warning, err := f.service.Add(f.device.ID, []uint{
		f.images[0].ID, f.images[0].ID, 999999, unsupported.ID, f.images[1].ID,
	})
	require.NoError(t, err)
	require.Empty(t, warning)
	require.Equal(t, []QueueAddRejection{
		{Index: 2, ImageID: 999999, Reason: "image_not_found"},
		{Index: 3, ImageID: unsupported.ID, Reason: "unsupported_source"},
	}, rejected)
	require.Len(t, items, 3)
	require.Equal(t, []uint{f.images[0].ID, f.images[0].ID, f.images[1].ID}, []uint{items[0].ImageID, items[1].ImageID, items[2].ImageID})
	require.Equal(t, []int{10, 20, 30}, []int{items[0].Position, items[1].Position, items[2].Position})
	for _, item := range items {
		require.Equal(t, model.QueueStatePending, item.State)
	}
}

func TestQueueAddRejectsMalformedSourcesByRequestIndex(t *testing.T) {
	f := newQueueManagementFixture(t)
	images := []model.Image{
		{Source: model.SourceGallery},
		{Source: model.SourceImmich},
		{Source: model.SourceSynologyPhotos, ExternalID: "not-a-number"},
		{Source: model.SourceUnsplash, FilePath: "file:///tmp/photo.jpg"},
	}
	require.NoError(t, f.db.Create(&images).Error)
	ids := []uint{f.images[0].ID, images[0].ID, images[1].ID, images[2].ID, images[3].ID, f.images[1].ID}

	items, rejected, _, err := f.service.Add(f.device.ID, ids)
	require.NoError(t, err)
	require.Len(t, items, 2)
	require.Equal(t, []QueueAddRejection{
		{Index: 1, ImageID: images[0].ID, Reason: "missing_source_identifier"},
		{Index: 2, ImageID: images[1].ID, Reason: "missing_source_identifier"},
		{Index: 3, ImageID: images[2].ID, Reason: "invalid_source_identifier"},
		{Index: 4, ImageID: images[3].ID, Reason: "invalid_source_identifier"},
	}, rejected)
}

func TestQueueAddRollsBackWholeAcceptedBatchOnPersistenceFailure(t *testing.T) {
	f := newQueueManagementFixture(t)
	require.NoError(t, f.db.Exec(fmt.Sprintf(`
		CREATE TRIGGER fail_queue_insert BEFORE INSERT ON device_image_queue
		WHEN NEW.image_id = %d BEGIN SELECT RAISE(ABORT, 'forced insert failure'); END`, f.images[1].ID)).Error)

	items, rejected, _, err := f.service.Add(f.device.ID, []uint{f.images[0].ID, f.images[1].ID, f.images[2].ID})
	require.Error(t, err)
	require.Nil(t, items)
	require.Empty(t, rejected)
	count, countErr := f.service.Count(f.device.ID)
	require.NoError(t, countErr)
	require.Zero(t, count)
}

func TestConcurrentQueueBatchesCommitAsContiguousOrderedGroups(t *testing.T) {
	f := newQueueManagementFixture(t)
	sqlDB, err := f.db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(4)

	batches := [][]uint{
		{f.images[0].ID, f.images[1].ID, f.images[0].ID},
		{f.images[2].ID, f.images[3].ID},
	}
	start := make(chan struct{})
	errs := make(chan error, len(batches))
	var wg sync.WaitGroup
	for _, batch := range batches {
		batch := batch
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, rejected, _, err := f.service.Add(f.device.ID, batch)
			if len(rejected) != 0 {
				errs <- fmt.Errorf("unexpected rejections: %v", rejected)
				return
			}
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	items, _, err := f.service.List(f.device.ID)
	require.NoError(t, err)
	require.Len(t, items, 5)
	gotIDs := make([]uint, len(items))
	for i, item := range items {
		gotIDs[i] = item.ImageID
		require.Equal(t, (i+1)*10, item.Position)
	}
	firstThenSecond := append(append([]uint{}, batches[0]...), batches[1]...)
	secondThenFirst := append(append([]uint{}, batches[1]...), batches[0]...)
	require.True(t, equalUintSlices(gotIDs, firstThenSecond) || equalUintSlices(gotIDs, secondThenFirst), gotIDs)
}

func TestQueueReorderUsesExactSnapshotAndKeepsInflightPositionsFixed(t *testing.T) {
	f := newQueueManagementFixture(t)
	first := f.createItem(t, 0, 10, model.QueueStatePending)
	claimed := f.createItem(t, 1, 20, model.QueueStateClaimed)
	last := f.createItem(t, 2, 30, model.QueueStateFailed)

	require.NoError(t, f.service.Reorder(f.device.ID, []uint{last.ID, first.ID}))
	items, _, err := f.service.List(f.device.ID)
	require.NoError(t, err)
	require.Equal(t, []uint{last.ID, claimed.ID, first.ID}, []uint{items[0].ID, items[1].ID, items[2].ID})
	require.Equal(t, []int{10, 20, 30}, []int{items[0].Position, items[1].Position, items[2].Position})
}

func TestQueueReorderConflictsNeverMutateOrder(t *testing.T) {
	tests := map[string]func(*queueManagementFixture, model.DeviceQueueItem, model.DeviceQueueItem, model.DeviceQueueItem) []uint{
		"stale snapshot": func(_ *queueManagementFixture, first, _, _ model.DeviceQueueItem) []uint { return []uint{first.ID} },
		"repeated id": func(_ *queueManagementFixture, first, _, _ model.DeviceQueueItem) []uint {
			return []uint{first.ID, first.ID}
		},
		"missing id": func(_ *queueManagementFixture, first, _, _ model.DeviceQueueItem) []uint {
			return []uint{first.ID, 999999}
		},
		"in-flight id": func(_ *queueManagementFixture, first, claimed, _ model.DeviceQueueItem) []uint {
			return []uint{first.ID, claimed.ID}
		},
		"foreign id": func(f *queueManagementFixture, first, _, _ model.DeviceQueueItem) []uint {
			other := model.Device{Name: "other"}
			require.NoError(t, f.db.Create(&other).Error)
			foreign := model.DeviceQueueItem{DeviceID: other.ID, ImageID: f.images[3].ID, Position: 10, Source: model.SourceGallery, State: model.QueueStatePending}
			require.NoError(t, f.db.Create(&foreign).Error)
			return []uint{first.ID, foreign.ID}
		},
	}
	for name, request := range tests {
		t.Run(name, func(t *testing.T) {
			f := newQueueManagementFixture(t)
			first := f.createItem(t, 0, 10, model.QueueStatePending)
			claimed := f.createItem(t, 1, 20, model.QueueStateClaimed)
			last := f.createItem(t, 2, 30, model.QueueStatePermanentlyInvalid)
			before, _, err := f.service.List(f.device.ID)
			require.NoError(t, err)

			err = f.service.Reorder(f.device.ID, request(f, first, claimed, last))
			require.ErrorIs(t, err, ErrQueueConflict)
			after, _, listErr := f.service.List(f.device.ID)
			require.NoError(t, listErr)
			require.Equal(t, before, after)
		})
	}
}

func TestQueueRemoveAndClearConflictWithoutPartialMutation(t *testing.T) {
	f := newQueueManagementFixture(t)
	pending := f.createItem(t, 0, 10, model.QueueStatePending)
	claimed := f.createItem(t, 1, 20, model.QueueStateClaimed)
	leased := f.createItem(t, 2, 30, model.QueueStateLeased)

	require.ErrorIs(t, f.service.Remove(f.device.ID, claimed.ID), ErrQueueConflict)
	require.ErrorIs(t, f.service.Remove(f.device.ID, leased.ID), ErrQueueConflict)
	deleted, err := f.service.Clear(f.device.ID)
	require.ErrorIs(t, err, ErrQueueConflict)
	require.Zero(t, deleted)
	items, _, listErr := f.service.List(f.device.ID)
	require.NoError(t, listErr)
	require.Equal(t, []uint{pending.ID, claimed.ID, leased.ID}, []uint{items[0].ID, items[1].ID, items[2].ID})

	require.NoError(t, f.service.Remove(f.device.ID, pending.ID))
	_, err = f.service.Clear(f.device.ID)
	require.ErrorIs(t, err, ErrQueueConflict)
}

func equalUintSlices(a, b []uint) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
