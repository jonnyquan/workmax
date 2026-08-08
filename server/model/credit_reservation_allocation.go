package model

import "server/globals"

// CreditReservationAllocation records exactly which credits pack rows funded a
// reservation so refunds can be applied back to the original packs instead of
// guessing from the user's current balance mix.
type CreditReservationAllocation struct {
	globals.GraMODEL
	ReservationID uint `json:"reservationId" gorm:"column:reservation_id;not null;index:idx_credit_reservation_allocation_reservation;comment:预留记录ID"`
	PackID        uint `json:"packId" gorm:"column:pack_id;not null;index:idx_credit_reservation_allocation_pack;comment:积分包ID"`
	Credits       int  `json:"credits" gorm:"column:credits;not null;default:0;comment:本包承担的积分数"`
}

func (CreditReservationAllocation) TableName() string {
	return "w_credit_reservation_allocation"
}
