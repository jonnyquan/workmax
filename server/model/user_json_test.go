package model

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestUserJSONNeverSerializesCredentials(t *testing.T) {
	user := User{
		Email:    "member@example.test",
		Password: "password-sentinel-must-not-leak",
		ApiKey:   "api-key-sentinel-must-not-leak",
	}

	encoded, err := json.Marshal(user)
	if err != nil {
		t.Fatalf("marshal User: %v", err)
	}
	payload := string(encoded)
	for _, forbidden := range []string{
		"password-sentinel-must-not-leak",
		"api-key-sentinel-must-not-leak",
		`"password"`,
		`"apiKey"`,
	} {
		if strings.Contains(payload, forbidden) {
			t.Errorf("serialized User contains credential material %q: %s", forbidden, payload)
		}
	}
	if !strings.Contains(payload, `"email":"member@example.test"`) {
		t.Errorf("serialized User lost safe profile fields: %s", payload)
	}
}
