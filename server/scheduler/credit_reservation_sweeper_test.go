package scheduler

import (
	"testing"
	"time"

	"gorm.io/gorm"

	"server/globals"
	"server/utils/testutil"
)

// installSystemDBFromTestutil swaps globals.GraDBs["system"] for a
// fresh testutil DB (which carries the w_credit_reservation schema)
// and restores the previous binding when the test exits.
//
// We can't share installSystemDB from tombstone_sweeper_test.go
// because that fixture only carries the tombstone table.
func installSystemDBFromTestutil(t *testing.T) {
	t.Helper()
	db := testutil.NewTestDB(t)
	prev := globals.GraDBs
	globals.GraDBs = map[string]*gorm.DB{"system": db}
	t.Cleanup(func() { globals.GraDBs = prev })
}

func TestCreditReservationSweeper_StopWithoutStartIsNoop(t *testing.T) {
	s := NewCreditReservationSweeper()
	stopWithin(t, s.Stop, time.Second)
}

func TestCreditReservationSweeper_StopAfterStartDoesNotDeadlock(t *testing.T) {
	installSystemDBFromTestutil(t)
	s := NewCreditReservationSweeper()
	s.Start()
	time.Sleep(20 * time.Millisecond)
	stopWithin(t, s.Stop, 2*time.Second)
}

func TestCreditReservationSweeper_StopIsIdempotent(t *testing.T) {
	installSystemDBFromTestutil(t)
	s := NewCreditReservationSweeper()
	s.Start()
	time.Sleep(20 * time.Millisecond)
	stopWithin(t, s.Stop, 2*time.Second)
	stopWithin(t, s.Stop, time.Second) // close-of-closed-channel guard
	stopWithin(t, s.Stop, time.Second)
}

func TestCreditReservationSweeper_StartIsIdempotent(t *testing.T) {
	installSystemDBFromTestutil(t)
	s := NewCreditReservationSweeper()
	s.Start()
	s.Start()
	s.Start()
	time.Sleep(20 * time.Millisecond)
	stopWithin(t, s.Stop, 2*time.Second)
}
