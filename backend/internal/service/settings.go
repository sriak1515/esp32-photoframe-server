package service

import (
	"sync"

	"github.com/aitjcize/esp32-photoframe-server/backend/internal/model"
	"github.com/aitjcize/esp32-photoframe-server/backend/pkg/googlephotos"
	"gorm.io/gorm"
)

// CalendarConfigProvider wraps SettingsService to provide calendar-specific OAuth config.
// It implements googlephotos.ConfigProvider so we can create a separate OAuth client for calendar.
type CalendarConfigProvider struct {
	settings *SettingsService
}

func (s *SettingsService) ImmichDatePolicy() (ImmichDatePolicy, error) {
	s.dateMu.RLock()
	defer s.dateMu.RUnlock()
	from, to, err := readImmichDatePair(s.db)
	if err != nil {
		return ImmichDatePolicy{}, err
	}
	return NewImmichDatePolicy(from, to)
}

// SetImmichDatePair validates and commits both bounds before notifying listeners.
func (s *SettingsService) SetImmichDatePair(from, to string) error {
	s.dateMu.Lock()
	defer s.dateMu.Unlock()
	return s.setImmichDatePair(from, to)
}

func (s *SettingsService) setImmichDatePair(from, to string) error {
	if _, err := NewImmichDatePolicy(from, to); err != nil {
		return err
	}
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		for key, value := range map[string]string{"immich_date_from": from, "immich_date_to": to} {
			if err := tx.Save(&model.Setting{Key: key, Value: value}).Error; err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	s.notifyChanged("immich_date_from", from)
	s.notifyChanged("immich_date_to", to)
	return nil
}

// UpdateImmichDatePair fills omitted bounds from the persisted pair before validating it.
func (s *SettingsService) UpdateImmichDatePair(from, to *string) error {
	s.dateMu.Lock()
	defer s.dateMu.Unlock()
	storedFrom, storedTo, err := readImmichDatePair(s.db)
	if err != nil {
		return err
	}
	if from != nil {
		storedFrom = *from
	}
	if to != nil {
		storedTo = *to
	}
	return s.setImmichDatePair(storedFrom, storedTo)
}

func readImmichDatePair(db *gorm.DB) (string, string, error) {
	var rows []model.Setting
	if err := db.Where("key IN ?", []string{"immich_date_from", "immich_date_to"}).Find(&rows).Error; err != nil {
		return "", "", err
	}
	var from, to string
	for _, row := range rows {
		if row.Key == "immich_date_from" {
			from = row.Value
		} else if row.Key == "immich_date_to" {
			to = row.Value
		}
	}
	return from, to, nil
}

func NewCalendarConfigProvider(s *SettingsService) *CalendarConfigProvider {
	return &CalendarConfigProvider{settings: s}
}

func (p *CalendarConfigProvider) GetGoogleConfig() (googlephotos.Config, error) {
	return p.settings.GetGoogleCalendarConfig()
}

type SettingsService struct {
	db        *gorm.DB
	mu        sync.Mutex
	dateMu    sync.RWMutex
	callbacks []func(key, value string)
}

func NewSettingsService(db *gorm.DB) *SettingsService {
	return &SettingsService{db: db}
}

func (s *SettingsService) Get(key string) (string, error) {
	var setting model.Setting
	result := s.db.First(&setting, "key = ?", key)
	if result.Error != nil {
		return "", result.Error
	}
	return setting.Value, nil
}

func (s *SettingsService) Set(key string, value string) error {
	if key == "immich_date_from" || key == "immich_date_to" {
		s.dateMu.Lock()
		defer s.dateMu.Unlock()
	}
	setting := model.Setting{Key: key, Value: value}
	// Save will create or update
	if err := s.db.Save(&setting).Error; err != nil {
		return err
	}
	s.notifyChanged(key, value)
	return nil
}

func (s *SettingsService) RegisterOnChange(callback func(key, value string)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.callbacks = append(s.callbacks, callback)
}

func (s *SettingsService) notifyChanged(key, value string) {
	s.mu.Lock()
	callbacks := append([]func(key, value string){}, s.callbacks...)
	s.mu.Unlock()

	for _, callback := range callbacks {
		callback(key, value)
	}
}

func (s *SettingsService) GetAll() (map[string]string, error) {
	var settings []model.Setting
	result := s.db.Find(&settings)
	if result.Error != nil {
		return nil, result.Error
	}

	settingsMap := make(map[string]string)
	for _, setting := range settings {
		settingsMap[setting.Key] = setting.Value
	}
	return settingsMap, nil
}

func (s *SettingsService) GetGoogleConfig() (googlephotos.Config, error) {
	clientID, _ := s.Get("google_client_id")
	clientSecret, _ := s.Get("google_client_secret")

	return googlephotos.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURL:  "", // Will be set dynamically
		Scopes: []string{
			"https://www.googleapis.com/auth/photospicker.mediaitems.readonly",
			"https://www.googleapis.com/auth/userinfo.email",
			"https://www.googleapis.com/auth/userinfo.profile",
		},
	}, nil
}

func (s *SettingsService) GetGoogleCalendarConfig() (googlephotos.Config, error) {
	clientID, _ := s.Get("google_client_id")
	clientSecret, _ := s.Get("google_client_secret")

	return googlephotos.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURL:  "", // Will be set dynamically
		Scopes: []string{
			"https://www.googleapis.com/auth/calendar.readonly",
			"https://www.googleapis.com/auth/userinfo.email",
			"https://www.googleapis.com/auth/userinfo.profile",
		},
	}, nil
}
