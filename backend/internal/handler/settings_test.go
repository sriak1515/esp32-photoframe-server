package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aitjcize/esp32-photoframe-server/backend/internal/model"
	"github.com/aitjcize/esp32-photoframe-server/backend/internal/service"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestUpdateSettingsRejectsInvalidDatePairBeforeAnyWrite(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:handler-settings?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Setting{}))
	settings := service.NewSettingsService(db)
	require.NoError(t, settings.SetImmichDatePair("2024-01-01", "2024-01-31"))
	require.NoError(t, settings.Set("unchanged", "old"))

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/settings", strings.NewReader(`{"settings":{"immich_date_from":"2024-02-01","unchanged":"new"}}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	ctx := e.NewContext(req, rec)
	err = NewHandler(settings, nil, nil, nil).UpdateSettings(ctx)
	require.Error(t, err)
	e.HTTPErrorHandler(err, ctx)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	from, _ := settings.Get("immich_date_from")
	to, _ := settings.Get("immich_date_to")
	other, _ := settings.Get("unchanged")
	require.Equal(t, "2024-01-01", from)
	require.Equal(t, "2024-01-31", to)
	require.Equal(t, "old", other)
}
