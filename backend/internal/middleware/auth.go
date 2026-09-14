package middleware

import (
	"net/http"
	"strings"

	"github.com/aitjcize/esp32-photoframe-server/backend/internal/service"
	"github.com/labstack/echo/v4"
)

type PrincipalType string

const (
	PrincipalAdministrator PrincipalType = "administrator"
	PrincipalDevice        PrincipalType = "device"

	principalContextKey = "authenticated_principal"
)

type AuthenticatedPrincipal struct {
	Type         PrincipalType
	UserID       uint
	Username     string
	TokenSubject string
	DeviceID     *uint
}

func GetPrincipal(c echo.Context) (AuthenticatedPrincipal, bool) {
	principal, ok := c.Get(principalContextKey).(AuthenticatedPrincipal)
	return principal, ok
}

// SetPrincipal is primarily useful for handlers invoked directly in tests.
func SetPrincipal(c echo.Context, principal AuthenticatedPrincipal) {
	c.Set(principalContextKey, principal)
}

func JWTMiddleware(authService *service.AuthService) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			// Extract token
			tokenString := extractToken(c)
			if tokenString == "" {
				return c.JSON(http.StatusUnauthorized, map[string]string{"error": "missing authentication token"})
			}

			// Validate token
			claims, err := authService.ValidateToken(tokenString)
			if err != nil {
				return c.JSON(http.StatusUnauthorized, map[string]string{"error": "invalid authentication token"})
			}

			principal := AuthenticatedPrincipal{
				UserID:       claims.UserID,
				Username:     claims.Username,
				TokenSubject: claims.Subject,
			}
			switch claims.Subject {
			case "":
				principal.Type = PrincipalAdministrator
			case "device":
				principal.Type = PrincipalDevice
				if claims.DeviceID > 0 {
					deviceID := claims.DeviceID
					principal.DeviceID = &deviceID
				}
			default:
				return c.JSON(http.StatusUnauthorized, map[string]string{"error": "invalid authentication token"})
			}
			SetPrincipal(c, principal)

			// Keep the existing scalar context values for handlers not yet migrated.
			c.Set("user_id", claims.UserID)
			c.Set("username", claims.Username)
			if claims.DeviceID > 0 {
				c.Set("device_id", claims.DeviceID)
			}

			return next(c)
		}
	}
}

func RequireAdministrator(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		principal, ok := GetPrincipal(c)
		if !ok {
			return c.JSON(http.StatusUnauthorized, map[string]string{"error": "authentication required"})
		}
		if principal.Type != PrincipalAdministrator {
			return c.JSON(http.StatusForbidden, map[string]string{"error": "administrator session required"})
		}
		return next(c)
	}
}

func RequireBoundDevice(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		principal, ok := GetPrincipal(c)
		if !ok {
			return c.JSON(http.StatusUnauthorized, map[string]string{"error": "authentication required"})
		}
		if principal.Type != PrincipalDevice || principal.DeviceID == nil || *principal.DeviceID == 0 {
			return c.JSON(http.StatusForbidden, map[string]string{"error": "bound device token required"})
		}
		return next(c)
	}
}

func extractToken(c echo.Context) string {
	authHeader := c.Request().Header.Get("Authorization")
	if strings.HasPrefix(authHeader, "Bearer ") {
		return strings.TrimPrefix(authHeader, "Bearer ")
	}

	token := c.QueryParam("token")
	if token != "" {
		return token
	}

	return ""
}
