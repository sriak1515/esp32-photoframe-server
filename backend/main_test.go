package main

import (
	"path/filepath"
	"testing"

	"github.com/aitjcize/esp32-photoframe-server/backend/internal/model"
	"github.com/aitjcize/esp32-photoframe-server/backend/internal/service"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestResolveJWTSecretGeneratesAndReusesPersistedSecret(t *testing.T) {
	t.Setenv("JWT_SECRET", "")
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "settings.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Setting{}))
	settings := service.NewSettingsService(db)

	generated := resolveJWTSecret(settings)
	require.Len(t, generated, 64)
	persisted, err := settings.Get("jwt_secret")
	require.NoError(t, err)
	require.Equal(t, generated, persisted)
	require.Equal(t, generated, resolveJWTSecret(service.NewSettingsService(db)))
}
