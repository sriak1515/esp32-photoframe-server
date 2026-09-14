package service

import (
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/aitjcize/esp32-photoframe-server/backend/internal/model"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func pollWithin(t *testing.T, queue *QueueService, deviceID uint) (QueuePollDecision, error) {
	t.Helper()
	type result struct {
		decision QueuePollDecision
		err      error
	}
	done := make(chan result, 1)
	go func() {
		decision, err := queue.Poll(deviceID)
		done <- result{decision: decision, err: err}
	}()
	select {
	case result := <-done:
		return result.decision, result.err
	case <-time.After(time.Second):
		t.Fatal("queue poll blocked with one database connection")
		return QueuePollDecision{}, nil
	}
}

type queueLifecycleFixture struct {
	db      *gorm.DB
	now     time.Time
	service *QueueService
	device  model.Device
	images  []model.Image
}

func newQueueLifecycleFixture(t *testing.T) *queueLifecycleFixture {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "queue.db")+"?_foreign_keys=on"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Device{}, &model.Image{}, &model.DeviceQueueItem{}, &model.DeviceHistory{}))
	f := &queueLifecycleFixture{db: db, now: time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)}
	f.service = NewQueueServiceWithOptions(db, QueueServiceOptions{
		Now:    func() time.Time { return f.now },
		Jitter: func(uint, int, time.Duration) time.Duration { return 0 },
	})
	f.device = model.Device{Name: "frame"}
	require.NoError(t, db.Create(&f.device).Error)
	for i := 0; i < 4; i++ {
		image := model.Image{Source: model.SourceGallery, FilePath: filepath.Join(t.TempDir(), "image")}
		require.NoError(t, db.Create(&image).Error)
		f.images = append(f.images, image)
	}
	return f
}

func (f *queueLifecycleFixture) add(t *testing.T, image int, position int, state string) model.DeviceQueueItem {
	t.Helper()
	item := model.DeviceQueueItem{DeviceID: f.device.ID, ImageID: f.images[image].ID, Position: position, Source: model.SourceGallery, State: state}
	require.NoError(t, f.db.Create(&item).Error)
	return item
}

func (f *queueLifecycleFixture) load(t *testing.T, id uint) model.DeviceQueueItem {
	t.Helper()
	var item model.DeviceQueueItem
	require.NoError(t, f.db.First(&item, id).Error)
	return item
}

func TestQueueLifecycleClaimLeaseReplayAndExpiry(t *testing.T) {
	f := newQueueLifecycleFixture(t)
	first := f.add(t, 0, 10, model.QueueStatePending)
	f.add(t, 1, 20, model.QueueStatePending)

	claim, err := f.service.Poll(f.device.ID)
	require.NoError(t, err)
	require.Equal(t, QueuePollClaim, claim.Action)
	require.Equal(t, first.ID, claim.Item.ID)
	require.Equal(t, 1, claim.Item.AttemptCount)
	require.NotNil(t, claim.Item.ClaimExpiresAt)

	blocked, err := f.service.Poll(f.device.ID)
	require.NoError(t, err)
	require.Equal(t, QueuePollBlocked, blocked.Action)
	require.Equal(t, first.ID, blocked.Item.ID)

	leased, err := f.service.LeaseClaim(f.device.ID, first.ID, *claim.Item.ClaimExpiresAt)
	require.NoError(t, err)
	require.Equal(t, f.now.Add(QueueLeaseDuration), *leased.LeaseExpiresAt)
	originalExpiry := *leased.LeaseExpiresAt
	require.ErrorIs(t, f.service.CompleteDelivery(f.device.ID, first.ID, originalExpiry), ErrQueueStaleLease,
		"an active lease cannot complete early")

	f.now = originalExpiry.Add(-time.Nanosecond)
	restarted := NewQueueServiceWithOptions(f.db, QueueServiceOptions{
		Now:    func() time.Time { return f.now },
		Jitter: func(uint, int, time.Duration) time.Duration { return 0 },
	})
	replay, err := restarted.Poll(f.device.ID)
	require.NoError(t, err)
	require.Equal(t, QueuePollReplay, replay.Action)
	require.Equal(t, first.ID, replay.Item.ID)
	require.Equal(t, 2, replay.Item.AttemptCount)
	replayedLease, err := restarted.LeaseClaim(f.device.ID, first.ID, *replay.Item.ClaimExpiresAt)
	require.NoError(t, err)
	require.Equal(t, originalExpiry, *replayedLease.LeaseExpiresAt, "replay must not extend the original lease")

	f.now = originalExpiry
	complete, err := f.service.Poll(f.device.ID)
	require.NoError(t, err)
	require.Equal(t, QueuePollComplete, complete.Action, "the exact expiry boundary is post-lease")
	require.NoError(t, f.service.CompleteDelivery(f.device.ID, first.ID, originalExpiry))
	require.ErrorIs(t, f.db.First(&model.DeviceQueueItem{}, first.ID).Error, gorm.ErrRecordNotFound)
	require.NoError(t, f.service.CompleteDelivery(f.device.ID, first.ID, originalExpiry))
}

func TestQueueLifecycleStaleClaimCAS(t *testing.T) {
	f := newQueueLifecycleFixture(t)
	item := f.add(t, 0, 10, model.QueueStatePending)
	claim, err := f.service.Poll(f.device.ID)
	require.NoError(t, err)
	stale := claim.Item.ClaimExpiresAt.Add(time.Second)

	_, err = f.service.LeaseClaim(f.device.ID, item.ID, stale)
	require.ErrorIs(t, err, ErrQueueStaleClaim)
	require.ErrorIs(t, f.service.FailClaim(f.device.ID, item.ID, stale, errors.New("temporary")), ErrQueueStaleClaim)
	require.Equal(t, model.QueueStateClaimed, f.load(t, item.ID).State)
}

func TestQueueLifecycleOfferTokenBlocksThenRecoversWithCAS(t *testing.T) {
	f := newQueueLifecycleFixture(t)
	first := f.add(t, 0, 10, model.QueueStatePending)
	second := f.add(t, 1, 20, model.QueueStatePending)
	claim, err := f.service.Poll(f.device.ID)
	require.NoError(t, err)
	offered, err := f.service.BeginLeaseOffer(f.device.ID, first.ID, *claim.Item.ClaimExpiresAt)
	require.NoError(t, err)

	blocked, err := f.service.Poll(f.device.ID)
	require.NoError(t, err)
	require.Equal(t, QueuePollBlocked, blocked.Action)
	f.now = *offered.ClaimExpiresAt
	recovered, err := f.service.Poll(f.device.ID)
	require.NoError(t, err)
	require.Equal(t, QueuePollClaim, recovered.Action)
	require.Equal(t, second.ID, recovered.Item.ID)
	failed := f.load(t, first.ID)
	require.Equal(t, model.QueueStateFailed, failed.State)
	require.Equal(t, "offer_expired", *failed.LastErrorCode)
	require.ErrorIs(t, f.service.ConfirmLeaseOffer(f.device.ID, first.ID, *offered.ClaimExpiresAt, *offered.LeaseExpiresAt), ErrQueueStaleLease)
	require.ErrorIs(t, f.service.FailLeaseOffer(f.device.ID, first.ID, *offered.ClaimExpiresAt, *offered.LeaseExpiresAt, errors.New("late write")), ErrQueueStaleLease)
}

func TestQueueLifecycleRetryBackoffDueSelectionAndNoReadyFallback(t *testing.T) {
	f := newQueueLifecycleFixture(t)
	first := f.add(t, 0, 10, model.QueueStatePending)
	second := f.add(t, 1, 20, model.QueueStatePending)
	claim, err := f.service.Poll(f.device.ID)
	require.NoError(t, err)
	require.NoError(t, f.service.FailClaim(f.device.ID, first.ID, *claim.Item.ClaimExpiresAt,
		RetryableQueueFailure("upstream timeout!", " temporary\n upstream failure ")))
	failed := f.load(t, first.ID)
	require.Equal(t, model.QueueStateFailed, failed.State)
	require.Equal(t, f.now.Add(3*time.Second), *failed.NextAttemptAt)
	require.Equal(t, "upstreamtimeout", *failed.LastErrorCode)
	require.Equal(t, "temporary upstream failure", *failed.LastError)

	next, err := f.service.Poll(f.device.ID)
	require.NoError(t, err)
	require.Equal(t, second.ID, next.Item.ID, "a retry-delayed head must not block later ready work")
	require.NoError(t, f.service.FailClaim(f.device.ID, second.ID, *next.Item.ClaimExpiresAt, errors.New("temporary")))

	none, err := f.service.Poll(f.device.ID)
	require.NoError(t, err)
	require.Equal(t, QueuePollNone, none.Action, "no ready queue item permits normal source fallback")

	f.now = *failed.NextAttemptAt
	due, err := f.service.Poll(f.device.ID)
	require.NoError(t, err)
	require.Equal(t, first.ID, due.Item.ID)
	require.Equal(t, 2, due.Item.AttemptCount)
}

func TestQueueLifecycleExpiredClaimRecoverySurvivesRestart(t *testing.T) {
	f := newQueueLifecycleFixture(t)
	first := f.add(t, 0, 10, model.QueueStatePending)
	second := f.add(t, 1, 20, model.QueueStatePending)
	claim, err := f.service.Poll(f.device.ID)
	require.NoError(t, err)
	f.now = claim.Item.ClaimExpiresAt.Add(time.Nanosecond)

	restarted := NewQueueServiceWithOptions(f.db, QueueServiceOptions{
		Now:    func() time.Time { return f.now },
		Jitter: func(uint, int, time.Duration) time.Duration { return 0 },
	})
	decision, err := restarted.Poll(f.device.ID)
	require.NoError(t, err)
	require.Equal(t, QueuePollClaim, decision.Action)
	require.Equal(t, second.ID, decision.Item.ID)
	recovered := f.load(t, first.ID)
	require.Equal(t, model.QueueStateFailed, recovered.State)
	require.Equal(t, "claim_expired", *recovered.LastErrorCode)
}

func TestQueueLifecyclePermanentInvalidClassificationAndRetry(t *testing.T) {
	f := newQueueLifecycleFixture(t)
	first := f.add(t, 0, 10, model.QueueStatePending)
	second := f.add(t, 1, 20, model.QueueStatePending)
	claim, err := f.service.Poll(f.device.ID)
	require.NoError(t, err)
	require.NoError(t, f.service.FailClaim(f.device.ID, first.ID, *claim.Item.ClaimExpiresAt,
		PermanentQueueFailure("missing relation", "image relation is missing")))
	invalid := f.load(t, first.ID)
	require.Equal(t, model.QueueStatePermanentlyInvalid, invalid.State)
	require.Nil(t, invalid.NextAttemptAt)

	next, err := f.service.Poll(f.device.ID)
	require.NoError(t, err)
	require.Equal(t, second.ID, next.Item.ID, "invalid work must not block later occurrences")
	require.NoError(t, f.service.FailClaim(f.device.ID, second.ID, *next.Item.ClaimExpiresAt, errors.New("temporary")))
	require.NoError(t, f.service.RetryInvalid(f.device.ID, first.ID))
	retried := f.load(t, first.ID)
	require.Equal(t, model.QueueStatePending, retried.State)
	require.Equal(t, 1, retried.AttemptCount)
	require.NotNil(t, retried.LastError, "retry preserves useful prior diagnostics")
	require.ErrorIs(t, f.service.RetryInvalid(f.device.ID, first.ID), ErrQueueItemNotFound)

	require.Equal(t, QueueFailurePermanentInvalid, ClassifyQueueError(gorm.ErrRecordNotFound).Category)
	require.Equal(t, QueueFailureRetryable, ClassifyQueueError(errors.New("network down")).Category)
}

func TestQueueRetryBackoffIsCappedAndJitterInjectable(t *testing.T) {
	f := newQueueLifecycleFixture(t)
	f.service.jitter = func(_ uint, _ int, window time.Duration) time.Duration { return window }
	require.Equal(t, 3750*time.Millisecond, f.service.retryDelay(1, 1))
	require.Equal(t, queueRetryMaxDelay, f.service.retryDelay(1, 9))
	require.Equal(t, queueRetryMaxDelay, f.service.retryDelay(1, 100))
	require.Equal(t, deterministicQueueJitter(42, 3, time.Second), deterministicQueueJitter(42, 3, time.Second))
}

func TestQueuePollUsesTransactionConnectionForPolicy(t *testing.T) {
	db := dateIntegrationDB(t)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	settings := NewSettingsService(db)
	require.NoError(t, settings.SetImmichDatePair("2024-01-01", "2024-12-31"))
	queue := NewQueueService(db)
	queue.SetImmichService(NewImmichService(db, settings), nil)
	device := model.Device{Name: "single connection"}
	require.NoError(t, db.Create(&device).Error)

	newItem := func(position int, state string) model.DeviceQueueItem {
		image := model.Image{Source: model.SourceImmich, ExternalID: fmt.Sprintf("asset-%d", position)}
		require.NoError(t, db.Create(&image).Error)
		item := model.DeviceQueueItem{DeviceID: device.ID, ImageID: image.ID, Source: image.Source, Position: position, State: state}
		require.NoError(t, db.Create(&item).Error)
		return item
	}

	pending := newItem(10, model.QueueStatePending)
	claim, err := pollWithin(t, queue, device.ID)
	require.NoError(t, err)
	require.Equal(t, pending.ID, claim.Item.ID)
	leased, err := queue.LeaseClaim(device.ID, pending.ID, *claim.Item.ClaimExpiresAt)
	require.NoError(t, err)
	replay, err := pollWithin(t, queue, device.ID)
	require.NoError(t, err)
	require.Equal(t, QueuePollReplay, replay.Action)
	require.Equal(t, leased.ID, replay.Item.ID)
	require.NoError(t, queue.FailClaim(device.ID, replay.Item.ID, *replay.Item.ClaimExpiresAt, errors.New("release replay")))

	require.NoError(t, settings.Set("immich_date_from", "malformed"))
	malformedPending := newItem(20, model.QueueStatePending)
	_, err = pollWithin(t, queue, device.ID)
	var policyErr *ImmichDatePolicyError
	require.ErrorAs(t, err, &policyErr)
	stored := loadQueueLifecycleItem(t, db, malformedPending.ID)
	require.Equal(t, model.QueueStatePending, stored.State)
	require.Zero(t, stored.AttemptCount)

	leaseExpiry := time.Now().Add(time.Hour).UTC()
	malformedReplay := newItem(5, model.QueueStateLeased)
	require.NoError(t, db.Model(&malformedReplay).Updates(map[string]interface{}{"lease_expires_at": leaseExpiry, "attempt_count": 4}).Error)
	_, err = pollWithin(t, queue, device.ID)
	require.ErrorAs(t, err, &policyErr)
	stored = loadQueueLifecycleItem(t, db, malformedReplay.ID)
	require.Equal(t, model.QueueStateLeased, stored.State)
	require.Equal(t, 4, stored.AttemptCount)
	require.Equal(t, leaseExpiry, *stored.LeaseExpiresAt)
}
