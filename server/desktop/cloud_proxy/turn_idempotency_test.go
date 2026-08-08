//go:build desktop

package cloud_proxy

import "testing"

func TestDesktopTurnRequestID(t *testing.T) {
	got, err := DesktopTurnRequestID(proxyTestTurnUUID)
	if err != nil {
		t.Fatalf("valid v4 UUID: %v", err)
	}
	if want := "desktop-turn:" + proxyTestTurnUUID; got != want || len(got) > 128 {
		t.Fatalf("request id=%q len=%d, want %q within 128 bytes", got, len(got), want)
	}

	for _, invalid := range []string{
		"",
		"DE305D54-75B4-431B-ADB2-EB6B9E546014",
		"de305d54-75b4-131b-adb2-eb6b9e546014",
		"de305d54-75b4-431b-7db2-eb6b9e546014",
		" de305d54-75b4-431b-adb2-eb6b9e546014",
	} {
		if _, err := DesktopTurnRequestID(invalid); err == nil {
			t.Errorf("accepted invalid turn UUID %q", invalid)
		}
	}
}
