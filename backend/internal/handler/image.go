package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"log"

	_ "image/jpeg"

	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/aitjcize/esp32-photoframe-server/backend/internal/imagesource"
	"github.com/aitjcize/esp32-photoframe-server/backend/internal/model"
	"github.com/aitjcize/esp32-photoframe-server/backend/internal/service"
	"github.com/aitjcize/esp32-photoframe-server/backend/pkg/gcalendar"
	"github.com/aitjcize/esp32-photoframe-server/backend/pkg/googlephotos"
	"github.com/aitjcize/esp32-photoframe-server/backend/pkg/photoframe"
	"github.com/aitjcize/esp32-photoframe-server/backend/pkg/weather"
	"github.com/labstack/echo/v4"
	"gorm.io/gorm"
)

// ImageHandlerDeps is the dependency bundle the handler needs at construction.
// Photo-library backends (synology, immich, google, on-disk gallery) live
// inside their respective imagesource plugins now — the handler itself
// only needs DB access for device lookup / history, the renderer for
// overlays, the processor for dithering, the weather/calendar clients for
// overlay data, the auth service, and the registered source registry.
type ImageHandlerDeps struct {
	Settings       *service.SettingsService
	Renderer       *service.RendererService
	Processor      *service.ProcessorService
	CalendarGoogle *googlephotos.Client
	Sources        *imagesource.Registry
	Weather        *weather.Client
	Calendar       *gcalendar.Client
	Auth           *service.AuthService
	DB             *gorm.DB
	DataDir        string
	QueueService   *service.QueueService
	QueueLoader    *service.QueueImageLoader
}

type ImageHandler struct {
	settings       *service.SettingsService
	renderer       *service.RendererService
	processor      *service.ProcessorService
	calendarGoogle *googlephotos.Client
	sources        *imagesource.Registry
	weather        *weather.Client
	calendar       *gcalendar.Client
	auth           *service.AuthService
	db             *gorm.DB
	dataDir        string
	queueService   *service.QueueService
	queueLoader    *service.QueueImageLoader
}

func NewImageHandler(deps ImageHandlerDeps) *ImageHandler {
	return &ImageHandler{
		settings:       deps.Settings,
		renderer:       deps.Renderer,
		processor:      deps.Processor,
		calendarGoogle: deps.CalendarGoogle,
		sources:        deps.Sources,
		weather:        deps.Weather,
		calendar:       deps.Calendar,
		auth:           deps.Auth,
		db:             deps.DB,
		dataDir:        deps.DataDir,
		queueService:   deps.QueueService,
		queueLoader:    deps.QueueLoader,
	}
}

// deviceBaseURL returns the base URL devices use to reach this server: the
// admin-configured "Server URL for devices" setting when set, otherwise
// derived from the incoming request (honoring X-Forwarded-Proto so URLs
// behind a TLS-terminating reverse proxy keep the https scheme).
func (h *ImageHandler) deviceBaseURL(c echo.Context) string {
	if base, err := h.settings.Get("device_image_base_url"); err == nil {
		if base = strings.TrimRight(strings.TrimSpace(base), "/"); base != "" {
			return base
		}
	}
	scheme := "http"
	if c.Request().TLS != nil || c.Request().Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + c.Request().Host
}

// identifyDevice resolves the requesting device SOLELY from its per-device
// token (the device_id set by the auth middleware). The previous X-Hostname /
// client-IP fallbacks were client-spoofable and have been removed — the token
// is the single source of truth. Returns the device and whether one was found.
func (h *ImageHandler) identifyDevice(c echo.Context) (model.Device, bool) {
	var device model.Device
	if devID, ok := c.Get("device_id").(uint); ok && devID > 0 {
		if err := h.db.First(&device, devID).Error; err == nil {
			return device, true
		}
	}
	return device, false
}

func (h *ImageHandler) ServeImage(c echo.Context) error {
	// 1. Identify the device from its token (the single source of truth).
	device, deviceFound := h.identifyDevice(c)

	// Record what the frame reports with each fetch (battery level and
	// firmware version), so the dashboard can show the last known state
	// even while the frame sleeps.
	if deviceFound {
		updates := map[string]interface{}{}
		if batt, err := strconv.Atoi(c.Request().Header.Get("X-Battery-Percentage")); err == nil &&
			batt >= 0 && batt <= 100 {
			updates["battery_level"] = batt
			updates["battery_reported_at"] = time.Now()
		}
		if fw := c.Request().Header.Get("X-Firmware-Version"); fw != "" && len(fw) <= 32 {
			updates["firmware_version"] = fw
		}
		if len(updates) > 0 {
			if uerr := h.db.Model(&model.Device{}).Where("id = ?", device.ID).Updates(updates).Error; uerr != nil {
				log.Printf("Failed to store reported state for device %d: %v", device.ID, uerr)
			}
		}
	}

	// The image source is ALWAYS the device's server-side assignment. The
	// /image/<source> path param is deliberately IGNORED so a device can never
	// be served a source that isn't assigned to it. (Legacy /image/<source>
	// URLs still route here for older firmware — the param is simply ignored.)
	// A device with no source set gets a 400 until it's configured server-side.
	source := ""
	if deviceFound {
		source = device.Source
	}
	if source == "" {
		return respondError(c, http.StatusBadRequest,
			"no image source configured for this device — set the device's Image Source in the server")
	}

	// Native resolution of the device panel
	nativeW, nativeH := 800, 480
	// Logical resolution for image generation (respects orientation)
	logicalW, logicalH := 800, 480

	showDate := false
	showPhotoDate := false
	showWeather := false
	var lat, lon float64

	if deviceFound {
		nativeW = device.Width
		nativeH = device.Height
		logicalW, logicalH = nativeW, nativeH

		showDate = device.ShowDate
		showPhotoDate = device.ShowPhotoDate
		showWeather = device.ShowWeather
		lat = device.WeatherLat
		lon = device.WeatherLon
	}

	// ALWAYS overrides logical resolution/orientation from Headers if present
	if wStr := c.Request().Header.Get("X-Display-Width"); wStr != "" {
		if w, err := strconv.Atoi(wStr); err == nil && w > 0 {
			logicalW = w
			nativeW = w
			if deviceFound && device.Width != w {
				device.Width = w
				h.db.Model(&device).Update("width", w)
			}
		}
	}
	if hStr := c.Request().Header.Get("X-Display-Height"); hStr != "" {
		if he, err := strconv.Atoi(hStr); err == nil && he > 0 {
			logicalH = he
			nativeH = he
			if deviceFound && device.Height != he {
				device.Height = he
				h.db.Model(&device).Update("height", he)
			}
		}
	}
	// Determine effective orientation from header or device config
	orientation := ""
	if deviceFound && device.ServerAuthoritative {
		orientation = device.Orientation
	} else if oStr := c.Request().Header.Get("X-Display-Orientation"); oStr != "" {
		orientation = oStr
		// Persist orientation update to database if it changed
		if deviceFound && device.Orientation != oStr {
			device.Orientation = oStr
			h.db.Model(&device).Where("server_authoritative = ?", false).Update("orientation", oStr)
		}
	} else if deviceFound {
		orientation = device.Orientation
	}

	// Swap logical dimensions to match orientation (used for overlays and collage)
	if orientation == "portrait" && logicalW > logicalH {
		logicalW, logicalH = logicalH, logicalW
	} else if orientation == "landscape" && logicalW < logicalH {
		logicalW, logicalH = logicalH, logicalW
	}

	// Load server-authoritative processing settings before deciding layout.
	var settings *photoframe.ProcessingSettings
	if deviceFound && device.DeviceProcessingSettings != "" && device.DeviceProcessingSettings != "{}" {
		settings = &photoframe.ProcessingSettings{}
		if err := json.Unmarshal([]byte(device.DeviceProcessingSettings), settings); err != nil {
			settings = nil
		}
	}

	layout := model.LayoutPhotoOverlay
	displayMode := "cover"
	backgroundColor := ""
	showCalendar := false
	firmwareVersion := c.Request().Header.Get("X-Firmware-Version")

	if deviceFound {
		if device.Layout != "" {
			layout = device.Layout
		}
		// Legacy server-side fields double as the fallback for firmware that
		// predates the device-synced scaleMode
		if device.DisplayMode != "" {
			displayMode = device.DisplayMode
		}
		backgroundColor = device.BackgroundColor
		showCalendar = device.ShowCalendar
	}
	if settings != nil && settings.ScaleMode != "" {
		displayMode = settings.ScaleMode
	}
	if settings != nil && settings.BackgroundColor != "" {
		backgroundColor = settings.BackgroundColor
	}

	deferredDeliveryAllowed := true
	// 1.2. Check queue first — serve queued images before falling back to source
	if deviceFound && h.queueService != nil && h.queueLoader != nil {
		queueItem, err := h.queueService.GetNextForDevice(device.ID)
		if err != nil {
			deferredDeliveryAllowed = false
			log.Printf("Queue check failed for device %d: %v", device.ID, err)
		} else if queueItem != nil {
			// Load image from queue
			img, loadErr := h.queueLoader.Load(queueItem)
			if loadErr != nil {
				deferredDeliveryAllowed = false
				var configErr *service.ImmichDatePolicyError
				if errors.As(loadErr, &configErr) {
					return respondError(c, http.StatusBadRequest, loadErr.Error())
				}
				// Preserve the entry: cache/network/decoding failures can be transient.
				log.Printf("Queue image load failed for device %d, image %d: %v", device.ID, queueItem.ImageID, loadErr)
			} else {
				// Get photo taken at
				var photoTakenAt *time.Time
				if device.ShowPhotoDate && queueItem.Image != nil {
					photoTakenAt = queueItem.Image.PhotoTakenAt
				}

				// Build and deliver the response before consuming the queue reference.
				if err := h.serveProcessedImage(c, device, deviceFound, img, photoTakenAt,
					logicalW, logicalH, nativeW, nativeH, orientation, layout, displayMode,
					showDate, showPhotoDate, showWeather, lat, lon, showCalendar,
					device.DateFormat, firmwareVersion); err != nil {
					return err
				}
				// Record history while the image FK is still live. History is ancillary:
				// a successful delivery still consumes the queue item if this write fails.
				h.completeQueuedDelivery(device.ID, queueItem.ID, queueItem.ImageID)
				return nil
			}
		}
	}

	var img image.Image
	var err error
	var photoTakenAt *time.Time

	// 1.5. Get Device History for Exclusion
	var excludeIDs []uint
	if deviceFound {
		// History retention: ensure we don't repeat recent 50 images
		// Get last 50 served images for this device
		var history []model.DeviceHistory
		if err := h.db.Where("device_id = ?", device.ID).
			Order("served_at desc").
			Limit(50).
			Find(&history).Error; err == nil {
			for _, h := range history {
				excludeIDs = append(excludeIDs, h.ImageID)
			}
		}
	}

	// All image sources — synthetic (AI, fractal, DLA) and library-backed
	// (gallery, immich, synology, google_photos, url_proxy) — flow through
	// the unified imagesource.Registry.
	if !h.sources.Has(source) {
		return respondError(c, http.StatusNotFound, "invalid source")
	}
	var devicePtr *model.Device
	if deviceFound {
		devicePtr = &device
	}
	sourceResp, err := h.sources.Fetch(source, &imagesource.Request{
		Device:       devicePtr,
		Source:       source,
		Width:        logicalW,
		Height:       logicalH,
		NativeWidth:  nativeW,
		NativeHeight: nativeH,
		Orientation:  orientation,
		ExcludeIDs:   excludeIDs,
	})
	if err != nil {
		if strings.Contains(err.Error(), "invalid source filter") {
			return respondError(c, http.StatusNotFound, "invalid source")
		}
		if errors.Is(err, gorm.ErrRecordNotFound) || strings.Contains(err.Error(), "record not found") {
			return respondError(c, http.StatusNotFound, "no photos found for this device")
		}
		return respondError(c, http.StatusInternalServerError, "failed to fetch photo: "+err.Error())
	}
	img = sourceResp.Image
	servedImageIDs := sourceResp.ImageIDs
	if sourceResp.PhotoTakenAt != nil {
		photoTakenAt = sourceResp.PhotoTakenAt
	}

	// If the source asked to bypass post-processing, encode straight to PNG
	// and ship it. The renderer overlay and epaper-image-convert pipeline
	// are skipped — the source already produced a panel-ready image, and
	// CDR / preprocessing would shift its flat color regions.
	if sourceResp.SkipPostProcessing {
		out := img
		if logicalW != nativeW || logicalH != nativeH {
			out = rotate90CW(out)
		}
		var buf bytes.Buffer
		if err := png.Encode(&buf, out); err != nil {
			return respondError(c, http.StatusInternalServerError, "png encode: "+err.Error())
		}
		body := buf.Bytes()

		delivery := h.applyConfigSync(c, &device, deviceFound, deferredDeliveryAllowed)

		c.Response().Header().Set("Content-Length", fmt.Sprintf("%d", len(body)))
		err := c.Blob(http.StatusOK, "image/png", body)
		h.completeConfigDelivery(delivery, err)
		return err
	}

	// 1.6. Record History
	if deviceFound && len(servedImageIDs) > 0 {
		go func(devID uint, imgIDs []uint) {
			rows := make([]model.DeviceHistory, 0, len(imgIDs))
			now := time.Now()
			for _, imgID := range imgIDs {
				if imgID == 0 {
					continue
				}
				rows = append(rows, model.DeviceHistory{
					DeviceID: devID,
					ImageID:  imgID,
					ServedAt: now,
				})
			}
			if len(rows) == 0 {
				return
			}
			// Insert + prune in a single transaction so we acquire the
			// SQLite write lock once instead of three times. The prune
			// finds the served_at of the 51st-newest row and range-deletes
			// anything older; both halves hit the
			// idx_device_histories_device_served composite index, so this
			// stays O(log n) instead of the O(n) scan the previous
			// "NOT IN (subquery)" form degraded into.
			h.db.Transaction(func(tx *gorm.DB) error {
				if err := tx.Create(&rows).Error; err != nil {
					return err
				}
				var cutoffs []time.Time
				if err := tx.Model(&model.DeviceHistory{}).
					Where("device_id = ?", devID).
					Order("served_at desc").
					Offset(50).
					Limit(1).
					Pluck("served_at", &cutoffs).Error; err != nil || len(cutoffs) == 0 {
					return nil
				}
				return tx.Where("device_id = ? AND served_at < ?", devID, cutoffs[0]).
					Delete(&model.DeviceHistory{}).Error
			})
		}(device.ID, servedImageIDs)
	}

	// 2. Render layout (photo + overlay + calendar)
	needsOverlay := showDate || showPhotoDate || showWeather || showCalendar
	var imgWithOverlay image.Image

	if needsOverlay {
		var weatherData *weather.CurrentWeather
		var deviceTimezone string
		if showWeather && lat != 0 && lon != 0 {
			latStr := fmt.Sprintf("%f", lat)
			lonStr := fmt.Sprintf("%f", lon)
			var weatherErr error
			weatherData, weatherErr = h.weather.GetWeather(latStr, lonStr)
			if weatherErr != nil {
				log.Printf("Failed to fetch weather data: %v", weatherErr)
			}
			if weatherData != nil {
				deviceTimezone = weatherData.Timezone
			}
		}

		var events []gcalendar.Event
		if showCalendar && h.calendar != nil && h.calendarGoogle != nil {
			httpClient, err := h.calendarGoogle.GetClient()
			if err == nil {
				calendarID := device.CalendarID
				if calendarID == "" {
					calendarID = "primary"
				}
				var calErr error
				events, calErr = h.calendar.GetTodayEvents(httpClient, calendarID, deviceTimezone)
				if calErr != nil {
					log.Printf("Failed to fetch calendar events: %v", calErr)
				}
			}
		}

		var renderErr error
		imgWithOverlay, renderErr = h.renderer.Render(service.RenderOptions{
			Layout:        layout,
			DisplayMode:   displayMode,
			Width:         logicalW,
			Height:        logicalH,
			NativeWidth:   nativeW,
			NativeHeight:  nativeH,
			Photo:         img,
			ShowDate:      showDate,
			ShowPhotoDate: showPhotoDate,
			PhotoDate:     photoTakenAt,
			ShowWeather:   showWeather,
			Weather:       weatherData,
			ShowCalendar:  showCalendar,
			Events:        events,
			Timezone:      deviceTimezone,
			DateFormat:    device.DateFormat,
		})
		if renderErr != nil {
			return respondError(c, http.StatusInternalServerError, "render failed: "+renderErr.Error())
		}
	} else {
		imgWithOverlay = img
	}

	// 3. Tone Mapping + Thumbnail (CLI)
	// Always pass native panel dimensions. The CLI handles orientation
	// internally (swaps dims, processes, rotates output to native layout).
	procOptions := map[string]string{
		"dimension": fmt.Sprintf("%dx%d", nativeW, nativeH),
	}
	if orientation != "" {
		procOptions["orientation"] = orientation
	}
	// Honor the device's fit/cover setting in the CLI. This matters when no
	// overlay is rendered: the raw photo goes straight to epaper-image-convert,
	// which would otherwise default to cover and crop. (With an overlay, the
	// renderer already composed the photo at panel dimensions, so the CLI
	// scale is a no-op either way.)
	procOptions["scale-mode"] = displayMode
	if displayMode == "fit" && backgroundColor != "" {
		procOptions["background-color"] = backgroundColor
	}

	// Determine output format based on firmware version (epdgz requires >= 2.6.1)
	firmwareVersion = c.Request().Header.Get("X-Firmware-Version")
	if firmwareVersion == "" || !photoframe.SupportsEPDGZ(firmwareVersion) {
		procOptions["format"] = "png"
	}

	// Processing settings were loaded above because they also determine layout.

	// 3.6. Load color palette from the server-side database.
	var palette *photoframe.Palette
	if deviceFound && device.DeviceColorPalette != "" && device.DeviceColorPalette != "{}" {
		palette = &photoframe.Palette{}
		if err := json.Unmarshal([]byte(device.DeviceColorPalette), palette); err != nil {
			palette = nil
		}
	}

	// GC16 grayscale mode: prefer the device-reported display type, falling back
	// to the board name for firmware that predates the display_type field.
	// Only treat as grayscale when the device is known.
	grayscale := deviceFound && device.IsGrayscale()

	headerOpts := h.processor.MapProcessingSettings(settings, palette, grayscale)
	for k, v := range headerOpts {
		procOptions[k] = v
	}

	log.Println("Processing image with options: ", procOptions)
	processedBytes, thumbBytes, err := h.processor.ProcessImage(imgWithOverlay, procOptions)
	if err != nil {
		fmt.Printf("Processor failed: %v\n", err)
		return respondError(c, http.StatusInternalServerError, "processor service failed: "+err.Error())
	}

	// 4. Cache Thumbnail & Set Headers
	if thumbBytes != nil {
		thumbID := fmt.Sprintf("%d", time.Now().UnixNano())
		thumbPath := filepath.Join(h.dataDir, fmt.Sprintf("thumb_%s.jpg", thumbID))

		if err := os.WriteFile(thumbPath, thumbBytes, 0644); err == nil {
			thumbnailUrl := fmt.Sprintf("%s/served-image-thumbnail/%s", h.deviceBaseURL(c), thumbID)
			c.Response().Header().Set("X-Thumbnail-URL", thumbnailUrl)
		} else {
			fmt.Printf("Failed to save served thumbnail: %v\n", err)
		}
	}

	// 5. Config Sync: push config payload if server has newer config
	delivery := h.applyConfigSync(c, &device, deviceFound, deferredDeliveryAllowed)

	// Set Content-Length header
	c.Response().Header().Set("Content-Length", fmt.Sprintf("%d", len(processedBytes)))

	contentType := "application/octet-stream"
	if firmwareVersion == "" || !photoframe.SupportsEPDGZ(firmwareVersion) {
		contentType = "image/png"
	}
	err = c.Blob(http.StatusOK, contentType, processedBytes)
	h.completeConfigDelivery(delivery, err)
	return err
}

func (h *ImageHandler) completeQueuedDelivery(deviceID, itemID, imageID uint) {
	if err := h.db.Create(&model.DeviceHistory{DeviceID: deviceID, ImageID: imageID, ServedAt: time.Now()}).Error; err != nil {
		log.Printf("Queue history write failed for device %d, image %d: %v", deviceID, imageID, err)
	}
	if err := h.queueService.Remove(deviceID, itemID); err != nil {
		log.Printf("Queue consumption cleanup failed for device %d, image %d: %v", deviceID, imageID, err)
	}
}

// SyncDeviceConfig handles device config sync.
// The device POSTs its current config; the server stores it and returns its own
// config if it's newer.
// POST /api/device-config/sync
func (h *ImageHandler) SyncDeviceConfig(c echo.Context) error {
	device, deviceFound := h.identifyDevice(c)
	if !deviceFound {
		return respondError(c, http.StatusNotFound, "device not found")
	}

	// Parse request body: { "config": {...}, "processing_settings": {...}, "color_palette": {...}, "config_last_updated": 123 }
	var req struct {
		Config             json.RawMessage `json:"config"`
		ProcessingSettings json.RawMessage `json:"processing_settings"`
		ColorPalette       json.RawMessage `json:"color_palette"`
		ConfigLastUpdated  int64           `json:"config_last_updated"`
	}
	if err := c.Bind(&req); err != nil {
		return respondError(c, http.StatusBadRequest, "invalid request")
	}

	if device.ServerAuthoritative {
		return c.JSON(http.StatusOK, map[string]interface{}{
			"status": "server_authoritative", "config_last_updated": device.ConfigLastUpdated,
		})
	}

	// Legacy reconciliation accepts firmware-managed fields when the device is newer.
	updates := map[string]interface{}{}
	if req.ConfigLastUpdated > device.ConfigLastUpdated {
		if len(req.Config) > 0 {
			updates["device_config"] = string(req.Config)
		}
		if len(req.ColorPalette) > 0 {
			updates["device_color_palette"] = string(req.ColorPalette)
		}
		if len(req.ProcessingSettings) > 0 {
			serialized, err := service.MergeFirmwareProcessing(device.DeviceProcessingSettings, string(req.ProcessingSettings))
			if err != nil {
				return respondError(c, http.StatusBadRequest, "invalid processing settings")
			}
			updates["device_processing_settings"] = string(serialized)
		}
		updates["config_last_updated"] = req.ConfigLastUpdated
	}

	if len(updates) > 0 {
		result := h.db.Model(&device).Where("server_authoritative = ?", false).Updates(updates)
		if result.Error != nil {
			return respondError(c, http.StatusInternalServerError, result.Error.Error())
		}
		if result.RowsAffected != 1 {
			return respondError(c, http.StatusConflict, "device became server-managed during sync")
		}
	}

	// Return server's config if it's newer
	resp := map[string]interface{}{
		"status":              "synced",
		"config_last_updated": device.ConfigLastUpdated,
	}

	return c.JSON(http.StatusOK, resp)
}

// UpdateDeviceConfig updates the server-side device config (called from web UI).
// PUT /api/devices/:id/config
func (h *ImageHandler) UpdateDeviceConfig(c echo.Context) error {
	id, _ := strconv.Atoi(c.Param("id"))

	var device model.Device
	if err := h.db.First(&device, uint(id)).Error; err != nil {
		return respondError(c, http.StatusNotFound, "device not found")
	}

	var req struct {
		Config             json.RawMessage        `json:"config"`
		ProcessingSettings json.RawMessage        `json:"processing_settings"`
		ColorPalette       json.RawMessage        `json:"color_palette"`
		Device             map[string]interface{} `json:"device"`
	}
	if err := c.Bind(&req); err != nil {
		return respondError(c, http.StatusBadRequest, "invalid request")
	}

	configRaw := json.RawMessage(device.DeviceConfig)
	processingRaw := json.RawMessage(device.DeviceProcessingSettings)
	paletteRaw := json.RawMessage(device.DeviceColorPalette)
	configProvided := len(req.Config) > 0
	processingProvided := len(req.ProcessingSettings) > 0
	paletteProvided := len(req.ColorPalette) > 0
	if len(req.Config) > 0 {
		configRaw = req.Config
	}
	if len(req.ProcessingSettings) > 0 {
		processingRaw = req.ProcessingSettings
	}
	if len(req.ColorPalette) > 0 {
		paletteRaw = req.ColorPalette
	}
	for _, raw := range []json.RawMessage{configRaw, processingRaw, paletteRaw} {
		if len(raw) == 0 || !json.Valid(raw) {
			return respondError(c, http.StatusBadRequest, "config, processing settings, and palette must be valid JSON")
		}
	}
	var configMap map[string]interface{}
	if err := json.Unmarshal(configRaw, &configMap); err != nil || configMap == nil {
		return respondError(c, http.StatusBadRequest, "config must be a JSON object")
	}
	for name, raw := range map[string]json.RawMessage{"processing settings": processingRaw, "palette": paletteRaw} {
		var object map[string]interface{}
		if err := json.Unmarshal(raw, &object); err != nil || object == nil {
			return respondError(c, http.StatusBadRequest, name+" must be a JSON object")
		}
	}
	generation := int64(0)
	if err := h.db.Transaction(func(tx *gorm.DB) error {
		var current model.Device
		if err := tx.First(&current, device.ID).Error; err != nil {
			return err
		}
		if !configProvided {
			configRaw = json.RawMessage(current.DeviceConfig)
		}
		if !processingProvided {
			processingRaw = json.RawMessage(current.DeviceProcessingSettings)
		}
		if !paletteProvided {
			paletteRaw = json.RawMessage(current.DeviceColorPalette)
		}
		if err := json.Unmarshal(configRaw, &configMap); err != nil || configMap == nil {
			return errors.New("config must be a JSON object")
		}
		for _, raw := range []json.RawMessage{processingRaw, paletteRaw} {
			var object map[string]interface{}
			if err := json.Unmarshal(raw, &object); err != nil || object == nil {
				return errors.New("settings documents must be JSON objects")
			}
		}
		generation = service.NextConfigGeneration(current.ConfigLastUpdated)
		rowUpdates := map[string]interface{}{}
		for _, key := range []string{"name", "host", "orientation", "enable_collage", "show_date", "show_photo_date", "show_weather", "weather_lat", "weather_lon", "ai_provider", "ai_model", "ai_prompt", "layout", "display_mode", "show_calendar", "calendar_id", "date_format", "source", "background_color", "server_authoritative"} {
			if value, exists := req.Device[key]; exists {
				rowUpdates[key] = value
			}
		}
		if authoritative, ok := rowUpdates["server_authoritative"].(bool); ok {
			current.ServerAuthoritative = authoritative
		}
		if name, ok := rowUpdates["name"].(string); ok {
			current.Name = name
		}
		// Matches the unified bare "/image", the legacy "/image/<source>",
		// and any "/image?..." query form.
		if imageURL, ok := configMap["image_url"].(string); ok &&
			(strings.Contains(imageURL, "/image/") ||
				strings.HasSuffix(imageURL, "/image") ||
				strings.Contains(imageURL, "/image?")) {
			// Generate or reuse a device token
			if userID, ok := c.Get("user_id").(uint); ok {
				username, _ := c.Get("username").(string)
				token, err := h.auth.GetOrGenerateDeviceTokenWithDB(tx, userID, username, current.Name, &current.ID)
				if err != nil {
					return err
				}
				configMap["access_token"] = token
			}
		}
		serializedConfig, err := json.Marshal(configMap)
		if err != nil {
			return err
		}
		rowUpdates["device_config"] = string(serializedConfig)
		rowUpdates["device_processing_settings"] = string(processingRaw)
		rowUpdates["device_color_palette"] = string(paletteRaw)
		rowUpdates["config_last_updated"] = generation
		rowUpdates["config_sync_pending"] = current.ServerAuthoritative
		result := tx.Model(&model.Device{}).Where("id = ?", current.ID).Updates(rowUpdates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		if err := tx.First(&device, current.ID).Error; err != nil {
			return err
		}
		return nil
	}); err != nil {
		return respondError(c, http.StatusInternalServerError, err.Error())
	}

	// Attempt to push directly to the device. Processing settings go FIRST:
	// the config push advances the device's sync timestamp, and a partial
	// failure after it would leave the settings undeliverable (the device
	// would report itself current on its next fetch and applyConfigSync
	// would skip the payload). When the processing push fails, the config
	// push is skipped too so the whole edit stays eligible for the deferred
	// sync path.
	pushResult := "pushed"
	if !device.ServerAuthoritative {
		pushResult = "legacy"
	}
	if device.ServerAuthoritative && device.Host == "" {
		pushResult = "pending"
	}
	if device.Host != "" && configMap != nil {
		client := photoframe.NewClient(device.Host)
		pushOK := true
		if device.ServerAuthoritative || len(req.ProcessingSettings) > 0 {
			if err := client.PushProcessingSettings(processingRaw); err != nil {
				log.Printf("Could not push processing settings to device %s: %v (will sync on next image fetch)", device.Host, err)
				pushOK = false
			}
		}
		if device.ServerAuthoritative {
			if err := client.PushPalette(paletteRaw); err != nil {
				log.Printf("Could not push palette to device %s: %v (will retry on next image fetch)", device.Host, err)
				pushOK = false
			}
		}
		if err := client.PushConfig(configMap); err != nil {
			log.Printf("Could not push config to device %s: %v (will sync on next image fetch)", device.Host, err)
			pushOK = false
		}
		if !pushOK {
			if device.ServerAuthoritative {
				pushResult = "pending"
			}
		}
		if device.ServerAuthoritative && pushOK {
			cleared, err := service.ClearConfigPending(h.db, device.ID, generation)
			if err != nil || !cleared {
				pushResult = "pending"
				if err == nil {
					_ = service.RetainCurrentConfigPending(h.db, device.ID, generation)
				}
			}
		}
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"status":              "updated",
		"push_result":         pushResult,
		"config_last_updated": generation,
		"config_sync_pending": device.ServerAuthoritative && pushResult == "pending",
	})
}

// GetDeviceConfig returns the server-side device config.
// GET /api/devices/:id/config
func (h *ImageHandler) GetDeviceConfig(c echo.Context) error {
	id, _ := strconv.Atoi(c.Param("id"))

	var device model.Device
	if err := h.db.First(&device, uint(id)).Error; err != nil {
		return respondError(c, http.StatusNotFound, "device not found")
	}

	resp := map[string]interface{}{
		"config_last_updated":  device.ConfigLastUpdated,
		"server_authoritative": device.ServerAuthoritative,
		"config_sync_pending":  device.ConfigSyncPending,
	}

	if device.DeviceConfig != "" && device.DeviceConfig != "{}" {
		resp["config"] = json.RawMessage(device.DeviceConfig)
	}
	if device.DeviceProcessingSettings != "" && device.DeviceProcessingSettings != "{}" {
		resp["processing_settings"] = json.RawMessage(device.DeviceProcessingSettings)
	}
	if device.DeviceColorPalette != "" && device.DeviceColorPalette != "{}" {
		resp["color_palette"] = json.RawMessage(device.DeviceColorPalette)
	}

	return c.JSON(http.StatusOK, resp)
}

// buildConfigPayload builds the X-Config-Payload JSON from device's stored config.
// Returns empty string if there's nothing to send.
func buildConfigPayload(device *model.Device) string {
	payload := map[string]json.RawMessage{}

	if device.DeviceConfig != "" && device.DeviceConfig != "{}" {
		payload["config"] = json.RawMessage(device.DeviceConfig)
	}
	if device.DeviceProcessingSettings != "" && device.DeviceProcessingSettings != "{}" {
		payload["processing_settings"] = json.RawMessage(device.DeviceProcessingSettings)
	}
	if device.DeviceColorPalette != "" && device.DeviceColorPalette != "{}" {
		payload["color_palette"] = json.RawMessage(device.DeviceColorPalette)
	}

	if len(payload) == 0 {
		return ""
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	return string(data)
}

func buildCompleteConfigPayload(device *model.Device) string {
	payload := map[string]json.RawMessage{
		"config":              json.RawMessage(device.DeviceConfig),
		"processing_settings": json.RawMessage(device.DeviceProcessingSettings),
		"color_palette":       json.RawMessage(device.DeviceColorPalette),
	}
	for key, raw := range payload {
		if len(raw) == 0 {
			payload[key] = json.RawMessage(`{}`)
		}
		if !json.Valid(payload[key]) {
			return ""
		}
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	return string(data)
}

func (h *ImageHandler) GetServedImageThumbnail(c echo.Context) error {
	id := c.Param("id")
	// Prevent directory traversal
	if id == "" || id == "." || id == ".." {
		return respondError(c, http.StatusBadRequest, "invalid id")
	}

	thumbPath := filepath.Join(h.dataDir, fmt.Sprintf("thumb_%s.jpg", id))
	data, err := os.ReadFile(thumbPath)
	if err != nil {
		if os.IsNotExist(err) {
			return respondError(c, http.StatusNotFound, "thumbnail not found")
		}
		return respondError(c, http.StatusInternalServerError, "failed to read thumbnail")
	}

	// Delete after 5 minutes instead of immediately
	go func() {
		time.Sleep(5 * time.Minute)
		os.Remove(thumbPath)
	}()

	// Set Content-Length header
	c.Response().Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))

	return c.Blob(http.StatusOK, "image/jpeg", data)
}

// postRotateWaitSec is how long we ask a device to stay awake after rotating so
// we can pull its config. The firmware clamps this to its own maximum.
const postRotateWaitSec = 20

// applyConfigSync reconciles config between server and device on each image
// fetch, using the device's reported X-Config-Last-Updated timestamp:
//   - server newer → push our config down via X-Config-Payload (device applies it)
//   - device newer → ask the device to stay up (X-Post-Rotate-Wait-Sec) and pull
//     its config in the background, catching the server up to on-device edits
//
// Shared by the bypass branch and the main flow.
type configDelivery struct {
	deviceID   uint
	generation int64
}

func (h *ImageHandler) completeConfigDelivery(delivery *configDelivery, responseErr error) {
	if delivery == nil || responseErr != nil {
		return
	}
	cleared, err := service.ClearConfigPending(h.db, delivery.deviceID, delivery.generation)
	if err != nil {
		log.Printf("Config sync: failed to clear pending for device %d: %v", delivery.deviceID, err)
	} else if !cleared {
		_ = service.RetainCurrentConfigPending(h.db, delivery.deviceID, delivery.generation)
	}
}

func (h *ImageHandler) applyConfigSync(c echo.Context, device *model.Device, deviceFound bool, allowCompletion bool) *configDelivery {
	if !deviceFound {
		return nil
	}

	deviceConfigTS := int64(0)
	if tsStr := c.Request().Header.Get("X-Config-Last-Updated"); tsStr != "" {
		if ts, err := strconv.ParseInt(tsStr, 10, 64); err == nil {
			deviceConfigTS = ts
		}
	}
	if device.ServerAuthoritative {
		if !device.ConfigSyncPending {
			return nil
		}
		payload := buildCompleteConfigPayload(device)
		if payload == "" {
			return nil
		}
		c.Response().Header().Set("X-Config-Payload", payload)
		if !allowCompletion {
			return nil
		}
		return &configDelivery{deviceID: device.ID, generation: device.ConfigLastUpdated}
	}

	// Device has edits we haven't captured: ask it to stay up and pull them.
	if deviceConfigTS > device.ConfigLastUpdated {
		if device.Host == "" {
			return nil // remote device we can't reach back to
		}
		c.Response().Header().Set("X-Post-Rotate-Wait-Sec", strconv.Itoa(postRotateWaitSec))
		h.pullDeviceConfigAsync(*device, deviceConfigTS)
		return nil
	}

	// Server has newer config: push it down. (Nothing to push if we've never
	// recorded a server-side edit, or the device is already current.)
	if device.ConfigLastUpdated <= 0 || device.ConfigLastUpdated <= deviceConfigTS {
		return nil
	}
	payload := buildConfigPayload(device)
	if payload == "" {
		return nil
	}
	c.Response().Header().Set("X-Config-Payload", payload)
	log.Printf("Config sync: pushing config to device (server=%d, device=%d)",
		device.ConfigLastUpdated, deviceConfigTS)
	return nil
}

// pullDeviceConfigAsync fetches the device's current config in the background and
// stores it, catching the server up when config was changed on the device. The
// device keeps its HTTP server up for postRotateWaitSec after rotating (it saw
// X-Post-Rotate-Wait-Sec), so we retry within that window until it answers.
func (h *ImageHandler) pullDeviceConfigAsync(device model.Device, deviceTS int64) {
	if device.Host == "" {
		return
	}
	go func() {
		client := photoframe.NewClient(device.Host)
		deadline := time.Now().Add(postRotateWaitSec * time.Second)
		for {
			configRaw, err := client.FetchConfig()
			if err == nil {
				updates := map[string]interface{}{
					"device_config":       configRaw,
					"config_last_updated": deviceTS,
				}
				if palette, perr := client.FetchPalette(); perr == nil {
					updates["device_color_palette"] = palette
				}
				var parsed struct {
					DisplayOrientation string `json:"display_orientation"`
				}
				if json.Unmarshal([]byte(configRaw), &parsed) == nil && parsed.DisplayOrientation != "" {
					updates["orientation"] = parsed.DisplayOrientation
				}
				if uerr := h.db.Model(&model.Device{}).Where("id = ? AND server_authoritative = ?", device.ID, false).Updates(updates).Error; uerr != nil {
					log.Printf("Config sync: failed to store pulled config for device %d: %v", device.ID, uerr)
				} else {
					log.Printf("Config sync: pulled newer config from device %s (ts=%d)", device.Host, deviceTS)
				}
				return
			}
			if time.Now().After(deadline) {
				log.Printf("Config sync: could not reach device %s to pull config within %ds",
					device.Host, postRotateWaitSec)
				return
			}
			time.Sleep(2 * time.Second)
		}
	}()
}

// rotate90CW returns src rotated 90° clockwise. Used for bypass sources to
// translate from the device's logical (oriented) layout to the panel's
// native physical layout.
func rotate90CW(src image.Image) *image.RGBA {
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	dst := image.NewRGBA(image.Rect(0, 0, sh, sw))
	for y := 0; y < sh; y++ {
		for x := 0; x < sw; x++ {
			dst.Set(sh-1-y, x, src.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return dst
}

// serveProcessedImage handles the post-processing pipeline (overlay rendering,
// e-paper processing, response writing) for both queue and source paths.
func (h *ImageHandler) serveProcessedImage(
	c echo.Context,
	device model.Device,
	deviceFound bool,
	img image.Image,
	photoTakenAt *time.Time,
	logicalW, logicalH, nativeW, nativeH int,
	orientation, layout, displayMode string,
	showDate, showPhotoDate, showWeather bool,
	lat, lon float64,
	showCalendar bool,
	dateFormat, firmwareVersion string,
) error {
	// 2. Render layout (photo + overlay + calendar)
	needsOverlay := showDate || showPhotoDate || showWeather || showCalendar
	var imgWithOverlay image.Image

	if needsOverlay {
		var weatherData *weather.CurrentWeather
		var deviceTimezone string
		if showWeather && lat != 0 && lon != 0 {
			latStr := fmt.Sprintf("%f", lat)
			lonStr := fmt.Sprintf("%f", lon)
			var weatherErr error
			weatherData, weatherErr = h.weather.GetWeather(latStr, lonStr)
			if weatherErr != nil {
				log.Printf("Failed to fetch weather data: %v", weatherErr)
			}
			if weatherData != nil {
				deviceTimezone = weatherData.Timezone
			}
		}

		var events []gcalendar.Event
		if showCalendar && h.calendar != nil && h.calendarGoogle != nil {
			httpClient, err := h.calendarGoogle.GetClient()
			if err == nil {
				calendarID := device.CalendarID
				if calendarID == "" {
					calendarID = "primary"
				}
				var calErr error
				events, calErr = h.calendar.GetTodayEvents(httpClient, calendarID, deviceTimezone)
				if calErr != nil {
					log.Printf("Failed to fetch calendar events: %v", calErr)
				}
			}
		}

		var renderErr error
		imgWithOverlay, renderErr = h.renderer.Render(service.RenderOptions{
			Layout:        layout,
			DisplayMode:   displayMode,
			Width:         logicalW,
			Height:        logicalH,
			NativeWidth:   nativeW,
			NativeHeight:  nativeH,
			Photo:         img,
			ShowDate:      showDate,
			ShowPhotoDate: showPhotoDate,
			PhotoDate:     photoTakenAt,
			ShowWeather:   showWeather,
			Weather:       weatherData,
			ShowCalendar:  showCalendar,
			Events:        events,
			Timezone:      deviceTimezone,
			DateFormat:    dateFormat,
		})
		if renderErr != nil {
			return respondError(c, http.StatusInternalServerError, "render failed: "+renderErr.Error())
		}
	} else {
		imgWithOverlay = img
	}

	// 3. Tone Mapping + Thumbnail (CLI)
	procOptions := map[string]string{
		"dimension": fmt.Sprintf("%dx%d", nativeW, nativeH),
	}
	if orientation != "" {
		procOptions["orientation"] = orientation
	}
	procOptions["scale-mode"] = displayMode
	if displayMode == "fit" && deviceFound && device.BackgroundColor != "" {
		procOptions["background-color"] = device.BackgroundColor
	}

	if firmwareVersion == "" || !photoframe.SupportsEPDGZ(firmwareVersion) {
		procOptions["format"] = "png"
	}

	// 3.5. Load processing settings from the server-side database
	var settings *photoframe.ProcessingSettings
	if deviceFound && device.DeviceProcessingSettings != "" && device.DeviceProcessingSettings != "{}" {
		settings = &photoframe.ProcessingSettings{}
		if err := json.Unmarshal([]byte(device.DeviceProcessingSettings), settings); err != nil {
			settings = nil
		}
	}

	// 3.6. Load color palette from the server-side database
	var palette *photoframe.Palette
	if deviceFound && device.DeviceColorPalette != "" && device.DeviceColorPalette != "{}" {
		palette = &photoframe.Palette{}
		if err := json.Unmarshal([]byte(device.DeviceColorPalette), palette); err != nil {
			palette = nil
		}
	}

	grayscale := deviceFound && device.IsGrayscale()

	headerOpts := h.processor.MapProcessingSettings(settings, palette, grayscale)
	for k, v := range headerOpts {
		procOptions[k] = v
	}

	log.Println("Processing image with options: ", procOptions)
	processedBytes, thumbBytes, err := h.processor.ProcessImage(imgWithOverlay, procOptions)
	if err != nil {
		fmt.Printf("Processor failed: %v\n", err)
		return respondError(c, http.StatusInternalServerError, "processor service failed: "+err.Error())
	}

	// 4. Cache Thumbnail & Set Headers
	if thumbBytes != nil {
		thumbID := fmt.Sprintf("%d", time.Now().UnixNano())
		thumbPath := filepath.Join(h.dataDir, fmt.Sprintf("thumb_%s.jpg", thumbID))

		if err := os.WriteFile(thumbPath, thumbBytes, 0644); err == nil {
			thumbnailUrl := fmt.Sprintf("%s/served-image-thumbnail/%s", h.deviceBaseURL(c), thumbID)
			c.Response().Header().Set("X-Thumbnail-URL", thumbnailUrl)
		} else {
			fmt.Printf("Failed to save served thumbnail: %v\n", err)
		}
	}

	// 5. Config Sync: push config payload if server has newer config
	delivery := h.applyConfigSync(c, &device, deviceFound, true)

	// Set Content-Length header
	c.Response().Header().Set("Content-Length", fmt.Sprintf("%d", len(processedBytes)))

	contentType := "application/octet-stream"
	if firmwareVersion == "" || !photoframe.SupportsEPDGZ(firmwareVersion) {
		contentType = "image/png"
	}
	err = c.Blob(http.StatusOK, contentType, processedBytes)
	h.completeConfigDelivery(delivery, err)
	return err
}
