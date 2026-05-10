package service

import (
	"fmt"
	"image"
	"net/http"

	"gorm.io/gorm"

	"github.com/aitjcize/esp32-photoframe-server/backend/internal/imagesource"
	"github.com/aitjcize/esp32-photoframe-server/backend/internal/model"
)

// urlProxySource is the registry plugin for "URL proxy" sources — arbitrary
// HTTP image URLs configured per-device. Unlike the photo-library sources
// it doesn't store anything in the images table; URLs live in url_sources
// with a device mapping, and we fetch one at request time.
type urlProxySource struct {
	db *gorm.DB
}

// NewURLProxySource constructs the plugin.
func NewURLProxySource(db *gorm.DB) imagesource.Source {
	return &urlProxySource{db: db}
}

func (s *urlProxySource) Name() string { return model.SourceURLProxy }

func (s *urlProxySource) Fetch(req *imagesource.Request) (*imagesource.Response, error) {
	var deviceID *uint
	if req.Device != nil {
		deviceID = &req.Device.ID
	}

	var picked model.URLSource
	query := s.db.Table("url_sources").Select("url_sources.id, url_sources.url").
		Joins("LEFT JOIN device_url_mappings ON url_sources.id = device_url_mappings.url_source_id")
	if deviceID != nil {
		query = query.Where("device_url_mappings.device_id = ? OR device_url_mappings.device_id IS NULL", *deviceID)
	} else {
		query = query.Where("device_url_mappings.device_id IS NULL")
	}
	if err := query.Order("RANDOM()").Limit(1).Scan(&picked).Error; err != nil {
		return nil, err
	}
	if picked.URL == "" {
		return nil, gorm.ErrRecordNotFound
	}

	resp, err := http.Get(picked.URL)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", picked.URL, err)
	}
	defer resp.Body.Close()

	img, _, err := image.Decode(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("decode %s: %w", picked.URL, err)
	}
	return &imagesource.Response{Image: img}, nil
}
