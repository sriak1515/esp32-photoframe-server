package service

import (
	"testing"
	"time"

	"github.com/aitjcize/esp32-photoframe-server/backend/internal/model"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func stringPtr(v string) *string { return &v }

func TestImmichDatePolicy(t *testing.T) {
	tests := []struct {
		name, from, to string
		dates          map[string]bool
	}{
		{"empty", "", "", map[string]bool{"": true, "2024-02-29": true}},
		{"same day", "2024-02-29", "2024-02-29", map[string]bool{"2024-02-28": false, "2024-02-29": true, "2024-03-01": false}},
		{"lower only", "2024-01-01", "", map[string]bool{"2023-12-31": false, "2024-01-01": true}},
		{"upper only", "", "2024-12-31", map[string]bool{"2024-12-31": true, "2025-01-01": false}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := NewImmichDatePolicy(tt.from, tt.to)
			require.NoError(t, err)
			for date, want := range tt.dates {
				var value *string
				if date != "" {
					value = stringPtr(date)
				}
				require.Equal(t, want, p.Eligible(value), date)
			}
		})
	}
	for _, pair := range [][2]string{{"2024-02-30", ""}, {"2024-3-01", ""}, {"2024-03-02", "2024-03-01"}} {
		_, err := NewImmichDatePolicy(pair[0], pair[1])
		require.Error(t, err)
	}
}

func TestImmichDatePolicyMaximumUpperBound(t *testing.T) {
	p, err := NewImmichDatePolicy("", "9999-12-31")
	require.NoError(t, err)
	require.True(t, p.Active())
	require.True(t, p.Eligible(stringPtr("9999-12-31")))
	_, upper := p.SearchBounds()
	require.Equal(t, "9999-12-31T23:59:59.999999999Z", upper)
}

func TestCaptureDatePrefersLocalWithoutOffsetConversion(t *testing.T) {
	require.Equal(t, "2024-03-01", *captureDate("2024-03-01T00:30:00", "2024-02-29T23:30:00-02:00"))
	require.Equal(t, "2024-02-29", *captureDate("", "2024-02-29T23:30:00-02:00"))
}

func TestImmichPolicyQualifiedQueryAndUnknownDate(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:policy?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Image{}))
	for _, date := range []*string{nil, stringPtr("2024-01-01"), stringPtr("2024-01-02")} {
		require.NoError(t, db.Create(&model.Image{Source: model.SourceImmich, PhotoTakenDate: date, CreatedAt: time.Now()}).Error)
	}
	p, err := NewImmichDatePolicy("2024-01-01", "2024-01-01")
	require.NoError(t, err)
	var images []model.Image
	require.NoError(t, p.Apply(db.Model(&model.Image{}), "images.photo_taken_date").Find(&images).Error)
	require.Len(t, images, 1)
}

func TestSettingsDatePairAtomicAndNotifiesAfterCommit(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:settings-pair?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Setting{}))
	s := NewSettingsService(db)
	require.NoError(t, s.SetImmichDatePair("2024-01-01", "2024-01-31"))
	notified := 0
	s.RegisterOnChange(func(key, value string) { notified++ })
	require.Error(t, s.SetImmichDatePair("2024-02-01", "2024-01-01"))
	require.Equal(t, 0, notified)
	from, _ := s.Get("immich_date_from")
	to, _ := s.Get("immich_date_to")
	require.Equal(t, "2024-01-01", from)
	require.Equal(t, "2024-01-31", to)
}

func TestSettingsPartialDateUpdateUsesPersistedCounterpart(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:settings-partial?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Setting{}))
	s := NewSettingsService(db)
	require.NoError(t, s.SetImmichDatePair("2024-01-01", "2024-01-31"))
	from := "2024-01-15"
	require.NoError(t, s.UpdateImmichDatePair(&from, nil))
	p, err := s.ImmichDatePolicy()
	require.NoError(t, err)
	require.True(t, p.Eligible(stringPtr("2024-01-31")))
	bad := "2024-02-01"
	require.Error(t, s.UpdateImmichDatePair(&bad, nil))
	stored, _ := s.Get("immich_date_from")
	require.Equal(t, from, stored)
}
