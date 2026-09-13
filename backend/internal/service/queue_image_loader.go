package service

import (
	"bytes"
	"fmt"
	"image"
	"net/http"
	"strconv"

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
	if item.Image == nil {
		return nil, fmt.Errorf("queue item has no image relation loaded")
	}

	img := item.Image

	switch item.Source {
	case model.SourceGallery, model.SourceGooglePhotos:
		return LoadLocalPhoto(l.dataDir, *img)

	case model.SourceImmich:
		return l.loadImmich(img)

	case model.SourceSynologyPhotos:
		return l.loadSynology(img)

	case model.SourceUnsplash, model.SourcePexels:
		return loadHTTPImage(img.FilePath)

	default:
		return nil, fmt.Errorf("unsupported source for queue: %s", item.Source)
	}
}

func (l *QueueImageLoader) loadImmich(img *model.Image) (image.Image, error) {
	if _, err := l.immichService.DatePolicy(); err != nil {
		return nil, err
	}
	// Try cache first
	if l.immichCache != nil {
		if cached := l.immichCache.Lookup(img.ID); cached != "" {
			if cachedImg, err := loadLocalImage(cached); err == nil {
				return cachedImg, nil
			}
		}
	}

	// Fall back to Immich download
	data, err := l.immichService.DownloadPhoto(img.ExternalID)
	if err != nil {
		return nil, fmt.Errorf("immich download: %w", err)
	}

	// Save to cache in background
	if l.immichCache != nil && l.immichCache.Enabled() {
		go func() {
			if _, cerr := l.immichCache.CacheImage(img.ID, img.ExternalID); cerr != nil {
				// already logged inside CacheImage
			}
		}()
	}

	decodedImg, _, err := image.Decode(bytes.NewReader(data))
	return decodedImg, err
}

func (l *QueueImageLoader) loadSynology(img *model.Image) (image.Image, error) {
	id, err := strconv.Atoi(img.ExternalID)
	if err != nil {
		return nil, fmt.Errorf("parse synology photo id: %w", err)
	}
	data, err := l.synologyService.DownloadPhoto(id)
	if err != nil {
		return nil, fmt.Errorf("synology download: %w", err)
	}
	decodedImg, _, err := image.Decode(bytes.NewReader(data))
	return decodedImg, err
}

func loadHTTPImage(url string) (image.Image, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, fmt.Errorf("http get: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http status %d", resp.StatusCode)
	}
	img, _, err := image.Decode(resp.Body)
	return img, err
}
