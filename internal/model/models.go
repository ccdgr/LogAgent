package model

import (
	"time"
	"gorm.io/gorm"
)

// Task 诊断任务模型
type Task struct {
	ID          string         `gorm:"primaryKey;type:varchar(64)" json:"id"`
	Description string         `gorm:"type:text" json:"description"`
	Status      string         `gorm:"type:varchar(20)" json:"status"` // pending, running, success, failed
	Result      string         `gorm:"type:text" json:"result"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	DeletedAt   gorm.DeletedAt `gorm:"index" json:"-"`
}

// TraceLog 推理轨迹日志
type TraceLog struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	TaskID    string    `gorm:"index;type:varchar(64)" json:"task_id"`
	Type      string    `gorm:"type:varchar(20)" json:"type"` // thought, action, observation
	Content   string    `gorm:"type:text" json:"content"`
	CreatedAt time.Time `json:"created_at"`
}
