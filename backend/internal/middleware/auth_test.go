package middleware

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/aitjcize/esp32-photoframe-server/backend/internal/model"
	"github.com/aitjcize/esp32-photoframe-server/backend/internal/service"
	"github.com/golang-jwt/jwt/v5"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestJWTMiddlewareClassifiesPrincipalsAndEnforcesPolicy(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "auth.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.APIKey{}, &model.UserSession{}))
	auth := service.NewAuthService(db, "test-secret")
	require.NoError(t, auth.Register("admin", "password"))
	adminToken, err := auth.Login("admin", "password", "test", "127.0.0.1")
	require.NoError(t, err)
	claims, err := auth.ValidateToken(adminToken)
	require.NoError(t, err)

	deviceID := uint(17)
	deviceToken, err := auth.GenerateDeviceToken(claims.UserID, claims.Username, "frame", &deviceID)
	require.NoError(t, err)
	unboundToken, err := auth.GenerateDeviceToken(claims.UserID, claims.Username, "unbound", nil)
	require.NoError(t, err)

	e := echo.New()
	seen := func(c echo.Context) error {
		principal, ok := GetPrincipal(c)
		require.True(t, ok)
		return c.JSON(http.StatusOK, principal)
	}
	e.GET("/admin", seen, JWTMiddleware(auth), RequireAdministrator)
	e.GET("/device", seen, JWTMiddleware(auth), RequireBoundDevice)

	tests := []struct {
		name, path, token string
		wantStatus        int
		wantType          PrincipalType
	}{
		{name: "administrator session", path: "/admin", token: adminToken, wantStatus: http.StatusOK, wantType: PrincipalAdministrator},
		{name: "device denied administration", path: "/admin", token: deviceToken, wantStatus: http.StatusForbidden},
		{name: "bound device", path: "/device", token: deviceToken, wantStatus: http.StatusOK, wantType: PrincipalDevice},
		{name: "unbound device", path: "/device", token: unboundToken, wantStatus: http.StatusForbidden},
		{name: "administrator denied firmware route", path: "/device", token: adminToken, wantStatus: http.StatusForbidden},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, test.path, nil)
			req.Header.Set(echo.HeaderAuthorization, "Bearer "+test.token)
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			require.Equal(t, test.wantStatus, rec.Code)
			if test.wantStatus == http.StatusOK {
				require.Contains(t, rec.Body.String(), string(test.wantType))
			}
		})
	}
}

func TestPublicLegacySecretForgeryWithExistingAPIKeyIsUnauthorized(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "legacy-auth.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.APIKey{}))
	auth := service.NewAuthService(db, "new-secret")
	boundID := uint(17)
	key := model.APIKey{UserID: 1, DeviceID: &boundID, Name: "legacy frame"}
	require.NoError(t, db.Create(&key).Error)
	forged := jwt.NewWithClaims(jwt.SigningMethodHS256, service.JWTClaims{
		UserID: 1, KeyID: key.ID, DeviceID: 999,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject: "device", ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
	})
	token, err := forged.SignedString([]byte("default-insecure-secret-change-me"))
	require.NoError(t, err)

	e := echo.New()
	e.GET("/image", func(c echo.Context) error { return c.NoContent(http.StatusOK) }, JWTMiddleware(auth), RequireBoundDevice)
	req := httptest.NewRequest(http.MethodGet, "/image", nil)
	req.Header.Set(echo.HeaderAuthorization, "Bearer "+token)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestJWTMiddlewareUsesCurrentDeviceBinding(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "binding.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.APIKey{}, &model.UserSession{}))
	auth := service.NewAuthService(db, "test-secret")
	require.NoError(t, auth.Register("admin", "password"))
	adminToken, err := auth.Login("admin", "password", "test", "127.0.0.1")
	require.NoError(t, err)
	claims, err := auth.ValidateToken(adminToken)
	require.NoError(t, err)

	originalID, reboundID := uint(1), uint(2)
	token, err := auth.GenerateDeviceToken(claims.UserID, claims.Username, "frame", &originalID)
	require.NoError(t, err)
	deviceClaims, err := auth.ValidateToken(token)
	require.NoError(t, err)
	require.NoError(t, auth.UpdateTokenDevice(claims.UserID, deviceClaims.KeyID, &reboundID))

	e := echo.New()
	e.GET("/", func(c echo.Context) error {
		principal, ok := GetPrincipal(c)
		require.True(t, ok)
		require.NotNil(t, principal.DeviceID)
		require.Equal(t, reboundID, *principal.DeviceID)
		return c.NoContent(http.StatusNoContent)
	}, JWTMiddleware(auth), RequireBoundDevice)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(echo.HeaderAuthorization, "Bearer "+token)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusNoContent, rec.Code)
}
