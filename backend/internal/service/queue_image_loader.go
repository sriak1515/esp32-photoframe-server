package service

import (
	"bytes"
	"image"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/aitjcize/esp32-photoframe-server/backend/internal/model"
	"gorm.io/gorm"
)

// QueueImageLoader loads images from queue items, dispatching to the correct
// source's loading logic based on the item's Source field.
type QueueImageLoader struct {
	db              *gorm.DB
	dataDir         string
	immichService   *ImmichService
	immichCache     *ImmichCacheService
	synologyService *SynologyService
}

var queueHTTPClient = &http.Client{Timeout: 30 * time.Second}

func validateQueueImage(img *model.Image) *QueueFailure {
	if img == nil {
		return PermanentQueueFailure("missing_image_relation", "queued image is no longer available")
	}
	switch img.Source {
	case model.SourceGallery, model.SourceGooglePhotos:
		if strings.TrimSpace(img.FilePath) == "" {
			return PermanentQueueFailure("missing_source_identifier", "queued image has no local file path")
		}
	case model.SourceImmich:
		if strings.TrimSpace(img.ExternalID) == "" {
			return PermanentQueueFailure("missing_source_identifier", "queued Immich image has no asset identifier")
		}
	case model.SourceSynologyPhotos:
		id, err := strconv.Atoi(img.ExternalID)
		if err != nil || id <= 0 {
			return PermanentQueueFailure("invalid_source_identifier", "queued Synology image has an invalid photo identifier")
		}
	case model.SourceUnsplash, model.SourcePexels:
		parsed, err := url.ParseRequestURI(img.FilePath)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return PermanentQueueFailure("invalid_source_identifier", "queued image has an invalid source URL")
		}
	default:
		return PermanentQueueFailure("unsupported_source", "queued image uses an unsupported source")
	}
	return nil
}

// NewQueueImageLoader constructs the loader.
func NewQueueImageLoader(
	db *gorm.DB,
	dataDir string,
	immichService *ImmichService,
	immichCache *ImmichCacheService,
	synologyService *SynologyService,
) *QueueImageLoader {
	return &QueueImageLoader{
		db:              db,
		dataDir:         dataDir,
		immichService:   immichService,
		immichCache:     immichCache,
		synologyService: synologyService,
	}
}

// Load loads the image bytes for a queue item and returns the decoded image.
func (l *QueueImageLoader) Load(item *model.DeviceQueueItem) (image.Image, error) {
	if item == nil {
		return nil, PermanentQueueFailure("missing_queue_item", "queued image occurrence is missing")
	}
	img := item.Image
	if img == nil {
		return nil, validateQueueImage(nil)
	}
	if item.Source != img.Source {
		return nil, PermanentQueueFailure("source_mismatch", "queued image source no longer matches its source snapshot")
	}
	if failure := validateQueueImage(img); failure != nil {
		return nil, failure
	}

	switch item.Source {
	case model.SourceGallery, model.SourceGooglePhotos:
		loaded, err := LoadLocalPhoto(l.dataDir, *img)
		if err != nil {
			return nil, RetryableQueueFailure("local_image_unavailable", "queued image file is temporarily unavailable")
		}
		return loaded, nil

	case model.SourceImmich:
		return l.loadImmich(item, img)

	case model.SourceSynologyPhotos:
		return l.loadSynology(img)

	case model.SourceUnsplash, model.SourcePexels:
		return loadHTTPImage(img.FilePath)

	default:
		return nil, PermanentQueueFailure("unsupported_source", "queued image uses an unsupported source")
	}
}

func (l *QueueImageLoader) loadImmich(item *model.DeviceQueueItem, img *model.Image) (image.Image, error) {
	if l.immichService == nil {
		return nil, RetryableQueueFailure("source_unavailable", "Immich service is temporarily unavailable")
	}
	if !item.PolicyValidated {
		if _, err := l.immichService.DatePolicy(); err != nil {
			return nil, err
		}
	}
	cacheCorrupt := false
	if l.immichCache != nil {
		if cached := l.immichCache.Lookup(img.ID); cached != "" {
			if cachedImg, err := loadLocalImage(cached); err == nil {
				return cachedImg, nil
			}
			cacheCorrupt = true
		}
	}

	data, err := l.immichService.DownloadPhoto(img.ExternalID)
	if err != nil {
		return nil, RetryableQueueFailure("upstream_unavailable", "Immich is temporarily unavailable for queued image delivery")
	}
	decodedImg, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, RetryableQueueFailure("upstream_invalid_image", "Immich temporarily returned unusable image data")
	}

	if l.immichCache != nil {
		go func(force bool) {
			if err := l.immichCache.CacheForQueueWithMode(img.ID, force); err != nil {
				// Delivery succeeded from upstream; a later request can retry repair.
			}
		}(cacheCorrupt)
	}
	return decodedImg, nil
}

func (l *QueueImageLoader) loadSynology(img *model.Image) (image.Image, error) {
	id, err := strconv.Atoi(img.ExternalID)
	if err != nil {
		return nil, PermanentQueueFailure("invalid_source_identifier", "queued Synology image has an invalid photo identifier")
	}
	if l.synologyService == nil {
		return nil, RetryableQueueFailure("source_unavailable", "Synology service is temporarily unavailable")
	}
	data, err := l.synologyService.DownloadPhoto(id)
	if err != nil {
		return nil, RetryableQueueFailure("upstream_unavailable", "Synology is temporarily unavailable for queued image delivery")
	}
	decodedImg, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, RetryableQueueFailure("upstream_invalid_image", "Synology temporarily returned unusable image data")
	}
	return decodedImg, nil
}

func loadHTTPImage(url string) (image.Image, error) {
	resp, err := queueHTTPClient.Get(url)
	if err != nil {
		return nil, RetryableQueueFailure("upstream_unavailable", "queued image source is temporarily unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
			return nil, PermanentQueueFailure("stale_source_relation", "queued image no longer exists at its source")
		}
		return nil, RetryableQueueFailure("upstream_unavailable", "queued image source is temporarily unavailable")
	}
	img, _, err := image.Decode(resp.Body)
	if err != nil {
		return nil, RetryableQueueFailure("upstream_invalid_image", "queued image source temporarily returned unusable image data")
	}
	return img, nil
}
