package service

import (
	"bytes"
	"image"

	"gorm.io/gorm"

	"github.com/aitjcize/esp32-photoframe-server/backend/internal/imagesource"
	"github.com/aitjcize/esp32-photoframe-server/backend/internal/model"
)

// synologyPhotosSource is the registry plugin for Synology-hosted photos.
type synologyPhotosSource struct {
	db       *gorm.DB
	synology *SynologyService
}

// NewSynologyPhotosSource constructs the plugin.
func NewSynologyPhotosSource(db *gorm.DB, synology *SynologyService) imagesource.Source {
	return &synologyPhotosSource{db: db, synology: synology}
}

func (s *synologyPhotosSource) Name() string { return model.SourceSynologyPhotos }

func (s *synologyPhotosSource) Fetch(req *imagesource.Request) (*imagesource.Response, error) {
	pick := func(orientation string, exclude []uint) (model.Image, error) {
		return PickRandomDBPhoto(s.db, model.SourceSynologyPhotos, orientation, exclude)
	}
	load := func(item model.Image) (image.Image, error) {
		data, err := s.synology.GetPhoto(item.SynologyPhotoID, item.ThumbnailKey, "large")
		if err != nil {
			return nil, err
		}
		img, _, err := image.Decode(bytes.NewReader(data))
		return img, err
	}
	return RunDBPhotoFlow(req, s.db, pick, load)
}
