package service

import (
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/aitjcize/esp32-photoframe-server/backend/internal/model"
)

var generativeTestDBCounter atomic.Int64

func setupGenerativeDB(t *testing.T) *gorm.DB {
	t.Helper()
	n := generativeTestDBCounter.Add(1)
	dsn := fmt.Sprintf("file:gen_test_%d?mode=memory&cache=shared", n)
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	if err := db.AutoMigrate(&model.GenerativeState{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func smallDevice() *model.Device {
	return &model.Device{ID: 1, Width: 80, Height: 48}
}

func TestGenerative_RequiresKnownSource(t *testing.T) {
	svc := NewGenerativeService(setupGenerativeDB(t))
	_, err := svc.Generate(1, "", 80, 48)
	assert.Error(t, err)
	_, err = svc.Generate(1, "nope", 80, 48)
	assert.Error(t, err)
}

func TestGenerative_FractalRendersAndPersists(t *testing.T) {
	db := setupGenerativeDB(t)
	svc := NewGenerativeService(db)
	dev := smallDevice()

	img, err := svc.Generate(dev.ID, model.SourceFractal, 80, 48)
	assert.NoError(t, err)
	assert.NotNil(t, img)

	var row model.GenerativeState
	err = db.Where("device_id = ? AND source = ?", dev.ID, model.SourceFractal).First(&row).Error
	assert.NoError(t, err)
	assert.NotEmpty(t, row.State)
}

func TestGenerative_StateAdvancesAcrossCalls(t *testing.T) {
	db := setupGenerativeDB(t)
	svc := NewGenerativeService(db)
	dev := smallDevice()

	_, err := svc.Generate(dev.ID, model.SourceFractal, 80, 48)
	assert.NoError(t, err)
	var first model.GenerativeState
	db.Where("device_id = ? AND source = ?", dev.ID, model.SourceFractal).First(&first)

	_, err = svc.Generate(dev.ID, model.SourceFractal, 80, 48)
	assert.NoError(t, err)
	var second model.GenerativeState
	db.Where("device_id = ? AND source = ?", dev.ID, model.SourceFractal).First(&second)

	assert.NotEqual(t, first.State, second.State)
}

func TestGenerative_DLAPersistsState(t *testing.T) {
	db := setupGenerativeDB(t)
	svc := NewGenerativeService(db)
	dev := smallDevice()

	img, err := svc.Generate(dev.ID, model.SourceDLA, 80, 48)
	assert.NoError(t, err)
	assert.NotNil(t, img)

	var row model.GenerativeState
	err = db.Where("device_id = ? AND source = ?", dev.ID, model.SourceDLA).First(&row).Error
	assert.NoError(t, err)
	// Sanity-check the binary blob: starts with the DLA1 magic in
	// little-endian order.
	assert.NotEmpty(t, row.State)
	assert.Equal(t, byte('1'), row.State[0])
	assert.Equal(t, byte('A'), row.State[1])
	assert.Equal(t, byte('L'), row.State[2])
	assert.Equal(t, byte('D'), row.State[3])
}

func TestGenerative_SourcesPerDeviceAreIndependent(t *testing.T) {
	db := setupGenerativeDB(t)
	svc := NewGenerativeService(db)
	dev := smallDevice()

	_, err := svc.Generate(dev.ID, model.SourceFractal, 80, 48)
	assert.NoError(t, err)
	_, err = svc.Generate(dev.ID, model.SourceDLA, 80, 48)
	assert.NoError(t, err)

	var rows []model.GenerativeState
	assert.NoError(t, db.Where("device_id = ?", dev.ID).Find(&rows).Error)
	assert.Len(t, rows, 2, "each source keeps its own state row")
}

func TestGenerative_ResetState(t *testing.T) {
	db := setupGenerativeDB(t)
	svc := NewGenerativeService(db)
	dev := smallDevice()

	_, err := svc.Generate(dev.ID, model.SourceFractal, 80, 48)
	assert.NoError(t, err)

	err = svc.ResetState(dev.ID, model.SourceFractal)
	assert.NoError(t, err)

	var count int64
	db.Model(&model.GenerativeState{}).
		Where("device_id = ? AND source = ?", dev.ID, model.SourceFractal).
		Count(&count)
	assert.Equal(t, int64(0), count)
}

func TestGenerative_HasAndSources(t *testing.T) {
	svc := NewGenerativeService(setupGenerativeDB(t))
	assert.True(t, svc.Has(model.SourceFractal))
	assert.True(t, svc.Has(model.SourceDLA))
	assert.False(t, svc.Has(model.SourceGooglePhotos))
	assert.False(t, svc.Has(""))

	assert.ElementsMatch(t,
		[]string{model.SourceFractal, model.SourceDLA},
		svc.Sources())
}

func TestGenerative_RejectsZeroDimensions(t *testing.T) {
	svc := NewGenerativeService(setupGenerativeDB(t))
	_, err := svc.Generate(1, model.SourceFractal, 0, 48)
	assert.Error(t, err)
	_, err = svc.Generate(1, model.SourceFractal, 80, 0)
	assert.Error(t, err)
}
