package main

import (
	"time"

	"gorm.io/gorm"
)

// AlertEventRecord 告警事件持久化模型（用于审计、回放、简历展示）
type AlertEventRecord struct {
	ID             uint      `json:"id" gorm:"primaryKey"`
	MachineID      string    `json:"machine_id" gorm:"size:128;index"`
	IP             string    `json:"ip" gorm:"size:64;index"`
	Level          string    `json:"level" gorm:"size:16;index"`
	Metric         string    `json:"metric" gorm:"size:64;index"`
	ThresholdValue float64   `json:"threshold_value"`
	CurrentValue   float64   `json:"current_value"`
	TriggerCount   int       `json:"trigger_count"`
	CooldownSec    int       `json:"cooldown_sec"`
	NotifyStatus   string    `json:"notify_status" gorm:"size:32;index"`
	NotifyError    string    `json:"notify_error" gorm:"type:text"`
	TriggeredAt    time.Time `json:"triggered_at" gorm:"index"`
	CreatedAt      time.Time `json:"created_at"`
}

func saveAlertEvent(db *gorm.DB, e AlertEventRecord) error {
	return db.Create(&e).Error
}
