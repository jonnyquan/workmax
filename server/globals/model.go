package globals

import (
	"time"
)

type GraMODEL struct {
	Id        uint      `gorm:"primarykey" json:"id"`                                                                                                                        // 主键ID
	CreatedAt time.Time `json:"createdAt" gorm:"column:created_at;type:datetime;not null;default:CURRENT_TIMESTAMP;autoCreateTime;comment:创建时间"`                             // 创建时间
	UpdatedAt time.Time `json:"updatedAt" gorm:"column:updated_at;type:datetime;not null;default:CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP;autoUpdateTime;comment:更新时间"` // 更新时间
}
