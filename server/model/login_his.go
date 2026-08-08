package model

import (
	"server/globals"
	"time"
)

type LoginHis struct {
	globals.GraMODEL
	UID        int       `json:"uid" gorm:"column:uid;type:int;not null;comment:用户ID"`
	IP         string    `json:"ip" gorm:"column:ip;type:varchar(100);not null;comment:IP地址"`
	DeviceType string    `json:"device_type" gorm:"column:device_type;type:varchar(100);not null;comment:设备类型"`
	DeviceName string    `json:"device_name" gorm:"column:device_name;type:varchar(100);not null;comment:设备名称"`
	LoginTime  time.Time `json:"login_time" gorm:"column:login_time;type:datetime;not null;comment:登录时间"`
	Location   string    `json:"location" gorm:"column:location;type:varchar(100);not null;comment:位置"`
}

func (LoginHis) TableName() string {
	return "w_login_his"
}
