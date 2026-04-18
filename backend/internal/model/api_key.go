package model

import "time"

type APIKey struct {
	ID        uint      `gorm:"primarykey" json:"id"`
	UserID    uint      `gorm:"index:idx_api_keys_user_id;index:idx_api_keys_user_device,priority:1" json:"user_id"`
	DeviceID  *uint     `gorm:"index:idx_api_keys_user_device,priority:2" json:"device_id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}
