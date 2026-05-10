package service

import (
	"errors"
	"fmt"
	"image"
	"time"

	"gorm.io/gorm"

	"github.com/aitjcize/esp32-photoframe-server/backend/internal/generative"
	"github.com/aitjcize/esp32-photoframe-server/backend/internal/generative/dla"
	"github.com/aitjcize/esp32-photoframe-server/backend/internal/generative/fractal"
	"github.com/aitjcize/esp32-photoframe-server/backend/internal/model"
)

// GenerativeService produces procedural images (fractal zoom, DLA growth, …)
// per device, persisting per-source state between calls so each call advances
// the visual sequence. The source name passed to Generate selects the
// generator and is also the key under which state is stored.
type GenerativeService struct {
	db   *gorm.DB
	gens map[string]generative.Generator
}

func NewGenerativeService(db *gorm.DB) *GenerativeService {
	return &GenerativeService{
		db: db,
		gens: map[string]generative.Generator{
			model.SourceFractal: fractal.Generator{},
			model.SourceDLA:     dla.Generator{},
		},
	}
}

// Sources returns the registered procedural source names, useful for
// surfacing valid values to the frontend or for source-availability checks.
func (s *GenerativeService) Sources() []string {
	out := make([]string, 0, len(s.gens))
	for k := range s.gens {
		out = append(out, k)
	}
	return out
}

// Has reports whether the given source name maps to a registered generator.
func (s *GenerativeService) Has(source string) bool {
	_, ok := s.gens[source]
	return ok
}

// Generate renders one frame at (width, height) for (deviceID, source),
// loading the previous state and persisting the new one.
func (s *GenerativeService) Generate(deviceID uint, source string, width, height int) (image.Image, error) {
	gen, ok := s.gens[source]
	if !ok {
		return nil, fmt.Errorf("generative: unsupported source %q", source)
	}
	if width <= 0 || height <= 0 {
		return nil, fmt.Errorf("generative: invalid dimensions %dx%d", width, height)
	}

	prev, err := s.loadState(deviceID, source)
	if err != nil {
		return nil, fmt.Errorf("generative: load state: %w", err)
	}

	img, next, err := gen.Generate(width, height, prev)
	if err != nil {
		return nil, fmt.Errorf("generative: %s: %w", source, err)
	}

	if err := s.saveState(deviceID, source, next); err != nil {
		// Don't fail the request just because we couldn't checkpoint —
		// the image is already rendered.
		fmt.Printf("WARN: generative: save state for device %d source %s: %v\n",
			deviceID, source, err)
	}
	return img, nil
}

func (s *GenerativeService) loadState(deviceID uint, source string) ([]byte, error) {
	var row model.GenerativeState
	err := s.db.Where("device_id = ? AND source = ?", deviceID, source).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return row.State, nil
}

func (s *GenerativeService) saveState(deviceID uint, source string, state []byte) error {
	row := model.GenerativeState{
		DeviceID:  deviceID,
		Source:    source,
		State:     state,
		UpdatedAt: time.Now(),
	}
	return s.db.Save(&row).Error
}

// ResetState clears the persisted state for a (device, source), so the next
// Generate call starts from a fresh seed.
func (s *GenerativeService) ResetState(deviceID uint, source string) error {
	return s.db.Where("device_id = ? AND source = ?", deviceID, source).
		Delete(&model.GenerativeState{}).Error
}
