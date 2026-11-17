package system

import (
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var AdminSoundKinds = []string{"recharge", "withdrawal", "verification", "registration"}

// 音频与计数分开更新；计数在业务事务内递增，不受补录时间、审核或删除影响。
type AdminSound struct {
	Kind        string `gorm:"primaryKey;size:32" json:"kind"`
	Revision    int64  `gorm:"not null;default:0" json:"revision"`
	Enabled     bool   `gorm:"not null;default:false" json:"enabled"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	AudioHash   string `json:"audio_hash"`
	Data        []byte `json:"-"`
}

func ValidAdminSoundKind(kind string) bool {
	for _, candidate := range AdminSoundKinds {
		if kind == candidate {
			return true
		}
	}
	return false
}

// 调用方必须提供业务事务，失败回滚时不产生声音提醒。
func RecordAdminSoundEvent(tx *gorm.DB, kind string) error {
	if !ValidAdminSoundKind(kind) {
		return fmt.Errorf("无效的声音提醒类型")
	}
	return tx.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "kind"}},
		DoUpdates: clause.Assignments(map[string]any{
			"revision": gorm.Expr("admin_sounds.revision + 1"),
		}),
	}).Create(&AdminSound{Kind: kind, Revision: 1}).Error
}

func CreateWithAdminSound(db *gorm.DB, record any, kind string) error {
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(record).Error; err != nil {
			return err
		}
		return RecordAdminSoundEvent(tx, kind)
	})
}
