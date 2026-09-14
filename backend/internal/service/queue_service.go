package service

import (
	"errors"
	"fmt"
	"hash/fnv"
	"strings"
	"time"

	"github.com/aitjcize/esp32-photoframe-server/backend/internal/model"
	"gorm.io/gorm"
)

const (
	queueSoftLimit      = 500
	QueueLeaseDuration  = 7 * time.Minute
	queueClaimDuration  = 3 * time.Minute
	queueRetryBaseDelay = 3 * time.Second
	queueRetryMaxDelay  = 15 * time.Minute
)

var (
	ErrQueueItemNotFound = errors.New("queue item not found")
	ErrQueueStaleClaim   = errors.New("queue claim is stale")
	ErrQueueStaleLease   = errors.New("queue lease is stale")
	ErrQueueConflict     = errors.New("queue conflict")
)

type QueueAddRejection struct {
	Index   int    `json:"index"`
	ImageID uint   `json:"image_id"`
	Reason  string `json:"reason"`
}

type QueuePollAction string

const (
	QueuePollNone     QueuePollAction = "none"
	QueuePollClaim    QueuePollAction = "claim"
	QueuePollReplay   QueuePollAction = "replay"
	QueuePollBlocked  QueuePollAction = "blocked"
	QueuePollComplete QueuePollAction = "complete"
)

type QueuePollDecision struct {
	Action QueuePollAction
	Item   *model.DeviceQueueItem
}

type QueueFailureCategory string

const (
	QueueFailureRetryable        QueueFailureCategory = "retryable"
	QueueFailurePermanentInvalid QueueFailureCategory = "permanent_invalid"
)

type QueueFailure struct {
	Category QueueFailureCategory
	Code     string
	Message  string
}

func (f *QueueFailure) Error() string { return f.Message }

func RetryableQueueFailure(code, message string) *QueueFailure {
	return &QueueFailure{Category: QueueFailureRetryable, Code: sanitizeErrorCode(code), Message: sanitizeQueueError(message)}
}

func PermanentQueueFailure(code, message string) *QueueFailure {
	return &QueueFailure{Category: QueueFailurePermanentInvalid, Code: sanitizeErrorCode(code), Message: sanitizeQueueError(message)}
}

func ClassifyQueueError(err error) *QueueFailure {
	if err == nil {
		return nil
	}
	var failure *QueueFailure
	if errors.As(err, &failure) {
		return failure
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return PermanentQueueFailure("missing_image", "queued image is no longer available")
	}
	return RetryableQueueFailure("delivery_error", "queued image delivery failed")
}

type QueueServiceOptions struct {
	Now    func() time.Time
	Jitter func(itemID uint, attempt int, window time.Duration) time.Duration
}

type QueueService struct {
	db     *gorm.DB
	immich *ImmichService
	cache  *ImmichCacheService
	now    func() time.Time
	jitter func(itemID uint, attempt int, window time.Duration) time.Duration
}

func NewQueueService(db *gorm.DB) *QueueService {
	return NewQueueServiceWithOptions(db, QueueServiceOptions{})
}

func NewQueueServiceWithOptions(db *gorm.DB, options QueueServiceOptions) *QueueService {
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Jitter == nil {
		options.Jitter = deterministicQueueJitter
	}
	return &QueueService{db: db, now: options.Now, jitter: options.Jitter}
}

func (s *QueueService) SetImmichService(immich *ImmichService, cache *ImmichCacheService) {
	s.immich, s.cache = immich, cache
}

// Poll atomically decides whether a device can claim initial work, reserve its
// leased occurrence for replay, must complete an expired lease, or has no work.
func (s *QueueService) Poll(deviceID uint) (QueuePollDecision, error) {
	now := s.now().UTC()
	decision := QueuePollDecision{Action: QueuePollNone}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var active model.DeviceQueueItem
		err := tx.Where("device_id = ? AND state IN ?", deviceID, []string{model.QueueStateClaimed, model.QueueStateLeased}).
			Order("position ASC, id ASC").First(&active).Error
		if err == nil {
			switch active.State {
			case model.QueueStateClaimed:
				if active.ClaimExpiresAt != nil && now.Before(*active.ClaimExpiresAt) {
					decision = QueuePollDecision{Action: QueuePollBlocked, Item: &active}
					return nil
				}
				failure := RetryableQueueFailure("claim_expired", "delivery processing did not finish before its claim expired")
				if active.ClaimExpiresAt == nil {
					failure = RetryableQueueFailure("invalid_claim", "delivery claim has no expiry")
					if err := failClaimWithoutDeadlineTx(tx, &active, failure, now, s.retryDelay(active.ID, active.AttemptCount)); err != nil {
						return err
					}
				} else if err := s.failClaimTx(tx, &active, *active.ClaimExpiresAt, failure, now); err != nil {
					return err
				}
			case model.QueueStateLeased:
				if active.ClaimExpiresAt != nil {
					if now.Before(*active.ClaimExpiresAt) {
						decision = QueuePollDecision{Action: QueuePollBlocked, Item: &active}
						return nil
					}
					failure := RetryableQueueFailure("offer_expired", "response offer did not finish before its deadline")
					if err := s.failLeaseOfferTx(tx, &active, *active.ClaimExpiresAt, failure, now); err != nil {
						return err
					}
					break
				}
				if active.LeaseExpiresAt == nil {
					failure := PermanentQueueFailure("invalid_lease", "queue lease has no expiry")
					if err := markLeaseInvalidTx(tx, &active, failure); err != nil {
						return err
					}
				} else if !now.Before(*active.LeaseExpiresAt) {
					decision = QueuePollDecision{Action: QueuePollComplete, Item: &active}
					return nil
				} else {
					if err := s.validateQueuePolicyTx(tx, active.Source); err != nil {
						return err
					}
					claimUntil := now.Add(queueClaimDuration)
					result := tx.Model(&model.DeviceQueueItem{}).
						Where("id = ? AND device_id = ? AND state = ? AND claim_expires_at IS NULL AND lease_expires_at = ?", active.ID, deviceID, model.QueueStateLeased, *active.LeaseExpiresAt).
						Updates(map[string]interface{}{"state": model.QueueStateClaimed, "claim_expires_at": claimUntil, "last_attempt_at": now, "attempt_count": gorm.Expr("attempt_count + 1")})
					if result.Error != nil {
						return result.Error
					}
					if result.RowsAffected != 1 {
						return ErrQueueStaleLease
					}
					active.State, active.ClaimExpiresAt = model.QueueStateClaimed, &claimUntil
					active.LastAttemptAt = &now
					active.AttemptCount++
					if err := tx.Preload("Image").First(&active, active.ID).Error; err != nil {
						return err
					}
					active.PolicyValidated = active.Source == model.SourceImmich && s.immich != nil
					decision = QueuePollDecision{Action: QueuePollReplay, Item: &active}
					return nil
				}
			}
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		var item model.DeviceQueueItem
		err = tx.Where("device_id = ? AND (state = ? OR (state = ? AND (next_attempt_at IS NULL OR next_attempt_at <= ?)))",
			deviceID, model.QueueStatePending, model.QueueStateFailed, now).
			Order("position ASC, id ASC").First(&item).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := s.validateQueuePolicyTx(tx, item.Source); err != nil {
			return err
		}
		claimUntil := now.Add(queueClaimDuration)
		query := tx.Model(&model.DeviceQueueItem{}).
			Where("id = ? AND device_id = ? AND state = ?", item.ID, deviceID, item.State)
		if item.State == model.QueueStateFailed {
			query = query.Where("next_attempt_at IS NULL OR next_attempt_at <= ?", now)
		}
		result := query.Updates(map[string]interface{}{
			"state": model.QueueStateClaimed, "claim_expires_at": claimUntil,
			"lease_expires_at": nil, "last_attempt_at": now,
			"attempt_count": gorm.Expr("attempt_count + 1"),
		})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrQueueStaleClaim
		}
		if err := tx.Preload("Image").First(&item, item.ID).Error; err != nil {
			return err
		}
		item.PolicyValidated = item.Source == model.SourceImmich && s.immich != nil
		decision = QueuePollDecision{Action: QueuePollClaim, Item: &item}
		return nil
	})
	if err != nil {
		return QueuePollDecision{}, fmt.Errorf("poll queue: %w", err)
	}
	return decision, nil
}

func (s *QueueService) validateQueuePolicyTx(tx *gorm.DB, source string) error {
	if source != model.SourceImmich || s.immich == nil {
		return nil
	}
	from, to, err := readImmichDatePair(tx)
	if err != nil {
		return err
	}
	_, err = NewImmichDatePolicy(from, to)
	return err
}

// RecordPrecacheResult exposes asynchronous preparation failures without
// changing delivery state. A successful retry clears only pre-cache diagnostics.
func (s *QueueService) RecordPrecacheResult(itemID uint, err error) {
	query := s.db.Model(&model.DeviceQueueItem{}).Where("id = ? AND state = ?", itemID, model.QueueStatePending)
	if err == nil {
		query.Where("last_error_code = ?", "upstream_unavailable").Updates(map[string]interface{}{"last_error_code": nil, "last_error": nil})
		return
	}
	var configErr *ImmichDatePolicyError
	if errors.As(err, &configErr) {
		return
	}
	failure := ClassifyQueueError(err)
	query.Updates(map[string]interface{}{"last_error_code": failure.Code, "last_error": failure.Message})
}

// BeginLeaseOffer revalidates claim ownership immediately before the response
// is offered. The retained claim token blocks concurrent replay until the
// response write is confirmed or failed.
func (s *QueueService) BeginLeaseOffer(deviceID, itemID uint, claimExpiresAt time.Time) (*model.DeviceQueueItem, error) {
	now := s.now().UTC()
	var item model.DeviceQueueItem
	staleReplay := false
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("id = ? AND device_id = ?", itemID, deviceID).First(&item).Error; err != nil {
			return err
		}
		leaseUntil := now.Add(QueueLeaseDuration)
		offerUntil := now.Add(queueClaimDuration)
		if item.LeaseExpiresAt != nil {
			if !now.Before(*item.LeaseExpiresAt) {
				result := tx.Model(&model.DeviceQueueItem{}).
					Where("id = ? AND device_id = ? AND state = ? AND claim_expires_at = ? AND lease_expires_at = ?",
						itemID, deviceID, model.QueueStateClaimed, claimExpiresAt, *item.LeaseExpiresAt).
					Updates(map[string]interface{}{"state": model.QueueStateLeased, "claim_expires_at": nil})
				if result.Error != nil {
					return result.Error
				}
				if result.RowsAffected != 1 {
					return ErrQueueStaleClaim
				}
				staleReplay = true
				return nil
			}
			leaseUntil = *item.LeaseExpiresAt
		}
		result := tx.Model(&model.DeviceQueueItem{}).
			Where("id = ? AND device_id = ? AND state = ? AND claim_expires_at = ? AND claim_expires_at > ?", itemID, deviceID, model.QueueStateClaimed, claimExpiresAt, now).
			Updates(map[string]interface{}{"state": model.QueueStateLeased, "claim_expires_at": offerUntil, "lease_expires_at": leaseUntil, "next_attempt_at": nil})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrQueueStaleClaim
		}
		if err := tx.Preload("Image").First(&item, itemID).Error; err != nil {
			return err
		}
		item.LeaseExpiresAt = &leaseUntil
		item.ClaimExpiresAt = &offerUntil
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("begin queue lease offer: %w", err)
	}
	if staleReplay {
		return nil, ErrQueueStaleLease
	}
	return &item, nil
}

// ConfirmLeaseOffer releases the response-offer token after a complete write.
func (s *QueueService) ConfirmLeaseOffer(deviceID, itemID uint, claimExpiresAt, leaseExpiresAt time.Time) error {
	result := s.db.Model(&model.DeviceQueueItem{}).
		Where("id = ? AND device_id = ? AND state = ? AND claim_expires_at = ? AND lease_expires_at = ?",
			itemID, deviceID, model.QueueStateLeased, claimExpiresAt, leaseExpiresAt).
		Update("claim_expires_at", nil)
	if result.Error != nil {
		return fmt.Errorf("confirm queue lease offer: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return ErrQueueStaleLease
	}
	return nil
}

// FailLeaseOffer makes a response-write failure retryable while requiring the
// exact offer and fixed lease tokens established before writing.
func (s *QueueService) FailLeaseOffer(deviceID, itemID uint, claimExpiresAt, leaseExpiresAt time.Time, deliveryErr error) error {
	failure := ClassifyQueueError(deliveryErr)
	if failure == nil {
		return errors.New("queue failure is required")
	}
	now := s.now().UTC()
	return s.db.Transaction(func(tx *gorm.DB) error {
		item := model.DeviceQueueItem{ID: itemID, DeviceID: deviceID, LeaseExpiresAt: &leaseExpiresAt}
		if err := tx.Model(&model.DeviceQueueItem{}).Select("attempt_count").Where("id = ?", itemID).Scan(&item.AttemptCount).Error; err != nil {
			return err
		}
		return s.failLeaseOfferTx(tx, &item, claimExpiresAt, failure, now)
	})
}

func (s *QueueService) failLeaseOfferTx(tx *gorm.DB, item *model.DeviceQueueItem, offerExpiresAt time.Time, failure *QueueFailure, now time.Time) error {
	next := now.Add(s.retryDelay(item.ID, item.AttemptCount))
	query := tx.Model(&model.DeviceQueueItem{}).
		Where("id = ? AND device_id = ? AND state = ? AND claim_expires_at = ?", item.ID, item.DeviceID, model.QueueStateLeased, offerExpiresAt)
	if item.LeaseExpiresAt != nil {
		query = query.Where("lease_expires_at = ?", *item.LeaseExpiresAt)
	}
	result := query.Updates(map[string]interface{}{
		"state": model.QueueStateFailed, "claim_expires_at": nil, "lease_expires_at": nil,
		"next_attempt_at": next, "last_error_code": failure.Code, "last_error": failure.Message,
	})
	if result.Error != nil {
		return fmt.Errorf("fail queue lease offer: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return ErrQueueStaleLease
	}
	return nil
}

// LeaseClaim is the production transition used by non-HTTP callers and tests.
// HTTP delivery uses BeginLeaseOffer and confirms only after writing.
func (s *QueueService) LeaseClaim(deviceID, itemID uint, claimExpiresAt time.Time) (*model.DeviceQueueItem, error) {
	item, err := s.BeginLeaseOffer(deviceID, itemID, claimExpiresAt)
	if err != nil {
		return nil, err
	}
	if err := s.ConfirmLeaseOffer(deviceID, itemID, *item.ClaimExpiresAt, *item.LeaseExpiresAt); err != nil {
		return nil, err
	}
	item.ClaimExpiresAt = nil
	return item, nil
}

func (s *QueueService) FailClaim(deviceID, itemID uint, claimExpiresAt time.Time, err error) error {
	failure := ClassifyQueueError(err)
	if failure == nil {
		return errors.New("queue failure is required")
	}
	now := s.now().UTC()
	return s.db.Transaction(func(tx *gorm.DB) error {
		var item model.DeviceQueueItem
		if err := tx.Where("id = ? AND device_id = ?", itemID, deviceID).First(&item).Error; err != nil {
			return err
		}
		if err := s.failClaimTx(tx, &item, claimExpiresAt, failure, now); err != nil {
			return fmt.Errorf("fail queue claim: %w", err)
		}
		return nil
	})
}

func (s *QueueService) failClaimTx(tx *gorm.DB, item *model.DeviceQueueItem, claimExpiresAt time.Time, failure *QueueFailure, now time.Time) error {
	state := model.QueueStateFailed
	var nextAttempt *time.Time
	if failure.Category == QueueFailurePermanentInvalid {
		state = model.QueueStatePermanentlyInvalid
	} else {
		next := now.Add(s.retryDelay(item.ID, item.AttemptCount))
		nextAttempt = &next
	}
	result := tx.Model(&model.DeviceQueueItem{}).
		Where("id = ? AND device_id = ? AND state = ? AND claim_expires_at = ?", item.ID, item.DeviceID, model.QueueStateClaimed, claimExpiresAt).
		Updates(map[string]interface{}{
			"state": state, "claim_expires_at": nil, "lease_expires_at": nil,
			"next_attempt_at": nextAttempt, "last_error_code": failure.Code, "last_error": failure.Message,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrQueueStaleClaim
	}
	return nil
}

// CompleteDelivery atomically records the best-effort delivery effect and
// removes an expired leased occurrence. A retry after the row was removed is
// successful only when the occurrence-keyed history record proves completion.
func (s *QueueService) CompleteDelivery(deviceID, itemID uint, leaseExpiresAt time.Time) error {
	now := s.now().UTC()
	var policy *ImmichDatePolicy
	var source string
	if err := s.db.Model(&model.DeviceQueueItem{}).Select("source").Where("id = ? AND device_id = ?", itemID, deviceID).Scan(&source).Error; err != nil {
		return fmt.Errorf("load queue source for completion: %w", err)
	}
	if source == model.SourceImmich && s.immich != nil {
		p, err := s.immich.DatePolicy()
		if err != nil {
			return fmt.Errorf("load Immich date policy for queue completion: %w", err)
		}
		policy = &p
	}

	immichCacheFilesMu.Lock()
	defer immichCacheFilesMu.Unlock()
	var staged []stagedCacheFile
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var item model.DeviceQueueItem
		err := tx.Where("id = ? AND device_id = ?", itemID, deviceID).First(&item).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			var history model.DeviceHistory
			if historyErr := tx.Where("queue_item_id = ? AND device_id = ?", itemID, deviceID).First(&history).Error; historyErr == nil {
				return nil
			} else if !errors.Is(historyErr, gorm.ErrRecordNotFound) {
				return historyErr
			}
			return ErrQueueItemNotFound
		}
		if err != nil {
			return err
		}
		if item.State != model.QueueStateLeased || item.LeaseExpiresAt == nil ||
			!item.LeaseExpiresAt.Equal(leaseExpiresAt) || now.Before(*item.LeaseExpiresAt) {
			return ErrQueueStaleLease
		}

		result := tx.Model(&model.DeviceQueueItem{}).
			Where("id = ? AND device_id = ? AND state = ? AND lease_expires_at = ? AND lease_expires_at <= ?",
				itemID, deviceID, model.QueueStateLeased, leaseExpiresAt, now).
			Update("state", model.QueueStateDelivered)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrQueueStaleLease
		}

		var history model.DeviceHistory
		historyErr := tx.Where("queue_item_id = ?", itemID).First(&history).Error
		if errors.Is(historyErr, gorm.ErrRecordNotFound) {
			queueItemID := itemID
			history = model.DeviceHistory{DeviceID: deviceID, ImageID: item.ImageID, QueueItemID: &queueItemID, ServedAt: now}
			if err := tx.Create(&history).Error; err != nil {
				return fmt.Errorf("record queue delivery history: %w", err)
			}
		} else if historyErr != nil {
			return historyErr
		} else if history.DeviceID != deviceID || history.ImageID != item.ImageID {
			return fmt.Errorf("queue occurrence %d has conflicting completion history", itemID)
		}

		result = tx.Where("id = ? AND device_id = ? AND state = ?", itemID, deviceID, model.QueueStateDelivered).
			Delete(&model.DeviceQueueItem{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrQueueStaleLease
		}
		files, err := cleanupFinalQueueReferencesTx(tx, []model.DeviceQueueItem{item}, policy)
		staged = append(staged, files...)
		if err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		if restoreErr := restoreCacheFiles(staged); restoreErr != nil {
			return fmt.Errorf("%v; restore staged cache files: %w", err, restoreErr)
		}
		return fmt.Errorf("complete queue delivery: %w", err)
	}
	if err := removeStagedCacheFilesFn(staged); err != nil {
		// The rename tombstone is durable and recoverStagedCacheFiles removes it
		// after restart because the cache row committed as deleted.
		fmt.Printf("remove staged queue cache files: %v\n", err)
	}
	return nil
}

func (s *QueueService) RetryInvalid(deviceID, itemID uint) error {
	result := s.db.Model(&model.DeviceQueueItem{}).
		Where("id = ? AND device_id = ? AND state = ?", itemID, deviceID, model.QueueStatePermanentlyInvalid).
		Updates(map[string]interface{}{"state": model.QueueStatePending, "claim_expires_at": nil, "lease_expires_at": nil, "next_attempt_at": nil})
	if result.Error != nil {
		return fmt.Errorf("retry invalid queue item: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return ErrQueueItemNotFound
	}
	return nil
}

func (s *QueueService) retryDelay(itemID uint, attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := queueRetryBaseDelay
	for i := 1; i < attempt && delay < queueRetryMaxDelay; i++ {
		delay *= 2
		if delay > queueRetryMaxDelay {
			delay = queueRetryMaxDelay
		}
	}
	window := delay / 4
	if delay >= queueRetryMaxDelay {
		window = 0
	}
	delay += s.jitter(itemID, attempt, window)
	if delay > queueRetryMaxDelay {
		return queueRetryMaxDelay
	}
	return delay
}

func deterministicQueueJitter(itemID uint, attempt int, window time.Duration) time.Duration {
	if window <= 0 {
		return 0
	}
	h := fnv.New64a()
	_, _ = fmt.Fprintf(h, "%d:%d", itemID, attempt)
	return time.Duration(h.Sum64() % uint64(window+1))
}

func sanitizeErrorCode(code string) string {
	code = strings.ToLower(strings.TrimSpace(code))
	var b strings.Builder
	for _, r := range code {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "delivery_error"
	}
	return b.String()
}

func sanitizeQueueError(message string) string {
	message = strings.Join(strings.Fields(message), " ")
	if len(message) > 240 {
		message = message[:240]
	}
	if message == "" {
		return "queued image delivery failed"
	}
	return message
}

func markLeaseInvalidTx(tx *gorm.DB, item *model.DeviceQueueItem, failure *QueueFailure) error {
	result := tx.Model(&model.DeviceQueueItem{}).
		Where("id = ? AND device_id = ? AND state = ? AND lease_expires_at IS NULL", item.ID, item.DeviceID, model.QueueStateLeased).
		Updates(map[string]interface{}{"state": model.QueueStatePermanentlyInvalid, "last_error_code": failure.Code, "last_error": failure.Message})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrQueueStaleLease
	}
	return nil
}

func failClaimWithoutDeadlineTx(tx *gorm.DB, item *model.DeviceQueueItem, failure *QueueFailure, now time.Time, delay time.Duration) error {
	next := now.Add(delay)
	result := tx.Model(&model.DeviceQueueItem{}).
		Where("id = ? AND device_id = ? AND state = ? AND claim_expires_at IS NULL", item.ID, item.DeviceID, model.QueueStateClaimed).
		Updates(map[string]interface{}{
			"state": model.QueueStateFailed, "next_attempt_at": next,
			"last_error_code": failure.Code, "last_error": failure.Message,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrQueueStaleClaim
	}
	return nil
}

// Add classifies every requested occurrence, then inserts all accepted
// occurrences as one contiguous transactionally safe batch.
func (s *QueueService) Add(deviceID uint, imageIDs []uint) ([]model.DeviceQueueItem, []QueueAddRejection, string, error) {
	distinctIDs := make([]uint, 0, len(imageIDs))
	seenIDs := make(map[uint]bool, len(imageIDs))
	for _, imageID := range imageIDs {
		if !seenIDs[imageID] {
			seenIDs[imageID] = true
			distinctIDs = append(distinctIDs, imageID)
		}
	}

	var images []model.Image
	if err := s.db.Where("id IN ?", distinctIDs).Find(&images).Error; err != nil {
		return nil, nil, "", fmt.Errorf("resolve queue images: %w", err)
	}
	imagesByID := make(map[uint]model.Image, len(images))
	for _, image := range images {
		imagesByID[image.ID] = image
	}

	accepted := make([]model.Image, 0, len(imageIDs))
	rejected := make([]QueueAddRejection, 0)
	hasImmich := false
	for index, imageID := range imageIDs {
		image, ok := imagesByID[imageID]
		if !ok {
			rejected = append(rejected, QueueAddRejection{Index: index, ImageID: imageID, Reason: "image_not_found"})
			continue
		}
		if !queueSourceSupported(image.Source) {
			rejected = append(rejected, QueueAddRejection{Index: index, ImageID: imageID, Reason: "unsupported_source"})
			continue
		}
		if failure := validateQueueImage(&image); failure != nil {
			rejected = append(rejected, QueueAddRejection{Index: index, ImageID: imageID, Reason: failure.Code})
			continue
		}
		accepted = append(accepted, image)
		hasImmich = hasImmich || image.Source == model.SourceImmich
	}

	if hasImmich && s.immich != nil {
		if _, err := s.immich.DatePolicy(); err != nil {
			return nil, rejected, "", err
		}
	}

	items := make([]model.DeviceQueueItem, 0, len(accepted))
	var warning string
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := lockQueueDeviceTx(tx, deviceID); err != nil {
			return err
		}

		var maxPosition int
		if err := tx.Model(&model.DeviceQueueItem{}).
			Where("device_id = ?", deviceID).
			Select("COALESCE(MAX(position), 0)").
			Scan(&maxPosition).Error; err != nil {
			return fmt.Errorf("get max position: %w", err)
		}

		var count int64
		if err := tx.Model(&model.DeviceQueueItem{}).Where("device_id = ?", deviceID).Count(&count).Error; err != nil {
			return fmt.Errorf("count queue items: %w", err)
		}

		for i, image := range accepted {
			item := model.DeviceQueueItem{
				DeviceID: deviceID,
				ImageID:  image.ID,
				Position: maxPosition + (i+1)*10,
				Source:   image.Source,
				State:    model.QueueStatePending,
			}
			if err := tx.Create(&item).Error; err != nil {
				return fmt.Errorf("create queue occurrence %d: %w", i, err)
			}
			items = append(items, item)
		}
		if count+int64(len(items)) >= queueSoftLimit {
			warning = fmt.Sprintf("Queue has reached soft limit of %d items", queueSoftLimit)
		}
		return nil
	})
	if err != nil {
		return nil, rejected, "", err
	}
	return items, rejected, warning, nil
}

// Remove deletes a single queue item by ID, verifying device ownership.
func (s *QueueService) Remove(deviceID uint, itemID uint) error {
	_, err := s.remove(deviceID, "device_id = ? AND id = ?", true, deviceID, itemID)
	return err
}

// RemoveByImageID removes a queue item by device and image ID.
func (s *QueueService) RemoveByImageID(deviceID uint, imageID uint) error {
	_, err := s.remove(deviceID, "device_id = ? AND image_id = ?", false, deviceID, imageID)
	return err
}

// Reorder reorders queue items. itemIDs is the new order (first = lowest position).
func (s *QueueService) Reorder(deviceID uint, itemIDs []uint) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := lockQueueDeviceTx(tx, deviceID); err != nil {
			return err
		}
		var current []model.DeviceQueueItem
		if err := tx.Where("device_id = ?", deviceID).Order("position ASC, id ASC").Find(&current).Error; err != nil {
			return fmt.Errorf("load queue snapshot: %w", err)
		}

		byID := make(map[uint]model.DeviceQueueItem, len(current))
		positions := make([]int, 0, len(current))
		maxPosition := 0
		for _, item := range current {
			byID[item.ID] = item
			if item.Position > maxPosition {
				maxPosition = item.Position
			}
			if queueItemReorderable(item.State) {
				positions = append(positions, item.Position)
			}
		}

		seen := make(map[uint]bool, len(itemIDs))
		for _, itemID := range itemIDs {
			if seen[itemID] {
				return fmt.Errorf("%w: repeated queue item id %d", ErrQueueConflict, itemID)
			}
			seen[itemID] = true
			item, ok := byID[itemID]
			if !ok {
				return fmt.Errorf("%w: queue item %d is stale or foreign", ErrQueueConflict, itemID)
			}
			if !queueItemReorderable(item.State) {
				return fmt.Errorf("%w: queue item %d is in flight", ErrQueueConflict, itemID)
			}
		}
		if len(itemIDs) != len(positions) {
			return fmt.Errorf("%w: reorder snapshot is stale", ErrQueueConflict)
		}
		for itemID, item := range byID {
			if queueItemReorderable(item.State) && !seen[itemID] {
				return fmt.Errorf("%w: reorder snapshot is missing queue item %d", ErrQueueConflict, itemID)
			}
		}

		for i, itemID := range itemIDs {
			result := tx.Model(&model.DeviceQueueItem{}).
				Where("device_id = ? AND id = ?", deviceID, itemID).
				Update("position", maxPosition+i+1)
			if result.Error != nil {
				return fmt.Errorf("temporarily move queue item %d: %w", itemID, result.Error)
			}
			if result.RowsAffected != 1 {
				return fmt.Errorf("%w: queue item %d changed", ErrQueueConflict, itemID)
			}
		}
		for i, itemID := range itemIDs {
			result := tx.Model(&model.DeviceQueueItem{}).
				Where("device_id = ? AND id = ?", deviceID, itemID).
				Update("position", positions[i])
			if result.Error != nil {
				return fmt.Errorf("reorder queue item %d: %w", itemID, result.Error)
			}
			if result.RowsAffected != 1 {
				return fmt.Errorf("%w: queue item %d changed", ErrQueueConflict, itemID)
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
		Order("position ASC, id ASC").
		Find(&items).Error
	if err != nil {
		return nil, 0, fmt.Errorf("list queue items: %w", err)
	}

	return items, count, nil
}

// Clear removes all queue items for a device. Returns the count of deleted items.
func (s *QueueService) Clear(deviceID uint) (int64, error) {
	return s.remove(deviceID, "device_id = ?", false, deviceID)
}

func (s *QueueService) remove(deviceID uint, where string, requireMatch bool, args ...interface{}) (int64, error) {
	var policy *ImmichDatePolicy
	if s.immich != nil {
		var sources []string
		if err := s.db.Model(&model.DeviceQueueItem{}).Where(where, args...).Distinct().Pluck("source", &sources).Error; err != nil {
			return 0, fmt.Errorf("load queue sources: %w", err)
		}
		for _, source := range sources {
			if source == model.SourceImmich {
				p, err := s.immich.DatePolicy()
				if err != nil {
					return 0, err
				}
				policy = &p
				break
			}
		}
	}
	immichCacheFilesMu.Lock()
	defer immichCacheFilesMu.Unlock()
	var staged []stagedCacheFile
	var removed int64
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := lockQueueDeviceTx(tx, deviceID); err != nil {
			return err
		}
		var items []model.DeviceQueueItem
		if err := tx.Where(where, args...).Find(&items).Error; err != nil {
			return fmt.Errorf("find queue items: %w", err)
		}
		if requireMatch && len(items) == 0 {
			return ErrQueueItemNotFound
		}
		for _, item := range items {
			if item.State == model.QueueStateClaimed || item.State == model.QueueStateLeased {
				return fmt.Errorf("%w: queue item %d is in flight", ErrQueueConflict, item.ID)
			}
		}
		result := tx.Where(where, args...).Delete(&model.DeviceQueueItem{})
		if result.Error != nil {
			return fmt.Errorf("remove queue items: %w", result.Error)
		}
		removed = result.RowsAffected
		files, err := cleanupFinalQueueReferencesTx(tx, items, policy)
		staged = append(staged, files...)
		if err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		if restoreErr := restoreCacheFiles(staged); restoreErr != nil {
			return 0, fmt.Errorf("%v; restore staged cache files: %w", err, restoreErr)
		}
		return 0, err
	}
	if err := removeStagedCacheFilesFn(staged); err != nil {
		fmt.Printf("remove staged queue cache files: %v\n", err)
	}
	return removed, nil
}

func lockQueueDeviceTx(tx *gorm.DB, deviceID uint) error {
	result := tx.Model(&model.Device{}).Where("id = ?", deviceID).UpdateColumn("id", gorm.Expr("id"))
	if result.Error != nil {
		return fmt.Errorf("lock queue device: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("lock queue device: device %d not found", deviceID)
	}
	return nil
}

func queueItemReorderable(state string) bool {
	return state == model.QueueStatePending || state == model.QueueStateFailed || state == model.QueueStatePermanentlyInvalid
}

func queueSourceSupported(source string) bool {
	switch source {
	case model.SourceGallery, model.SourceGooglePhotos, model.SourceImmich, model.SourceSynologyPhotos, model.SourceUnsplash, model.SourcePexels:
		return true
	default:
		return false
	}
}

func cleanupFinalQueueReferencesTx(tx *gorm.DB, items []model.DeviceQueueItem, policy *ImmichDatePolicy) ([]stagedCacheFile, error) {
	if policy == nil {
		return nil, nil
	}
	var staged []stagedCacheFile
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
			return staged, fmt.Errorf("load queued image for cleanup: %w", err)
		}
		if image.Source != model.SourceImmich || policy.Eligible(image.PhotoTakenDate) {
			continue
		}
		var refs, memberships int64
		if err := tx.Model(&model.DeviceQueueItem{}).Where("image_id = ?", image.ID).Count(&refs).Error; err != nil {
			return staged, err
		}
		if err := tx.Model(&model.ImageAlbumMembership{}).Where("image_id = ?", image.ID).Count(&memberships).Error; err != nil {
			return staged, err
		}
		if refs > 0 || memberships > 0 {
			continue
		}
		var caches []model.ImmichCache
		if err := tx.Where("image_id = ?", image.ID).Find(&caches).Error; err != nil {
			return staged, err
		}
		paths := make([]string, 0, len(caches))
		for _, cache := range caches {
			paths = append(paths, cache.FilePath)
		}
		files, err := stageCacheFiles(paths)
		if err != nil {
			return staged, fmt.Errorf("stage cache files: %w", err)
		}
		staged = append(staged, files...)
		if len(caches) > 0 {
			if err := tx.Unscoped().Delete(&caches).Error; err != nil {
				return staged, err
			}
		}
		if err := tx.Unscoped().Delete(&image).Error; err != nil {
			return staged, err
		}
	}
	return staged, nil
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
		Where("device_id = ? AND image_id = ? AND state <> ?", deviceID, imageID, model.QueueStateDelivered).
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
		Where("device_id = ? AND image_id IN ? AND state <> ?", deviceID, imageIDs, model.QueueStateDelivered).
		Distinct().
		Pluck("image_id", &queued).Error
	if err != nil {
		return nil, fmt.Errorf("check queued images: %w", err)
	}
	return queued, nil
}
