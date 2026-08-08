package model

import (
	"reflect"
	"strings"
	"sync"
	"testing"

	"gorm.io/gorm/schema"
)

func TestCommerceProviderEventTerminalStates(t *testing.T) {
	for _, test := range []struct {
		status   string
		terminal bool
	}{
		{CommerceProviderEventStatusReceived, false},
		{CommerceProviderEventStatusProcessing, false},
		{CommerceProviderEventStatusRetryWait, false},
		{CommerceProviderEventStatusProcessed, true},
		{CommerceProviderEventStatusIgnored, true},
		{CommerceProviderEventStatusManualReview, false},
	} {
		if got := (CommerceProviderEvent{Status: test.status}).IsTerminal(); got != test.terminal {
			t.Fatalf("status %q terminal = %v, want %v", test.status, got, test.terminal)
		}
	}
}

func TestCommerceModelsPinMigrationOwnedColumnTypesAndDatabaseTimestamps(t *testing.T) {
	tests := []struct {
		name      string
		value     any
		wantTypes map[string]string
	}{
		{
			name:  "provider event",
			value: &CommerceProviderEvent{},
			wantTypes: map[string]string{
				"id":                      "bigint unsigned",
				"created_at":              "datetime(6)",
				"updated_at":              "datetime(6)",
				"provider":                "varchar(32) character set ascii collate ascii_bin",
				"provider_account_id":     "varchar(255) character set ascii collate ascii_bin",
				"provider_api_version":    "varchar(32) character set ascii collate ascii_bin",
				"event_id":                "varchar(255) character set ascii collate ascii_bin",
				"event_type":              "varchar(128) character set ascii collate ascii_bin",
				"object_id":               "varchar(255) character set ascii collate ascii_bin",
				"live_mode":               "tinyint unsigned",
				"provider_created_at":     "datetime(6)",
				"verification_key_digest": "char(71) character set ascii collate ascii_bin",
				"payload_digest":          "char(64) character set ascii collate ascii_bin",
				"payload_json":            "mediumblob",
				"status":                  "varchar(32) character set ascii collate ascii_bin",
				"attempt_count":           "int unsigned",
				"processing_version":      "bigint unsigned",
				"lease_owner_id":          "varchar(128) character set ascii collate ascii_bin",
				"lease_expires_at":        "datetime(6)",
				"next_attempt_at":         "datetime(6)",
				"processed_at":            "datetime(6)",
				"outcome_kind":            "varchar(64) character set ascii collate ascii_bin",
				"result_digest":           "char(64) character set ascii collate ascii_bin",
				"last_error_code":         "varchar(64) character set ascii collate ascii_bin",
			},
		},
		{
			name:  "outbox",
			value: &CommerceOutbox{},
			wantTypes: map[string]string{
				"id":                "bigint unsigned",
				"created_at":        "datetime(6)",
				"updated_at":        "datetime(6)",
				"provider_event_id": "bigint unsigned",
				"ordinal":           "int unsigned",
				"topic":             "varchar(128) character set ascii collate ascii_bin",
				"dedupe_key":        "char(64) character set ascii collate ascii_bin",
				"payload_digest":    "char(64) character set ascii collate ascii_bin",
				"payload_json":      "mediumblob",
				"status":            "varchar(32) character set ascii collate ascii_bin",
				"available_at":      "datetime(6)",
				"delivery_attempts": "bigint unsigned",
				"dispatch_version":  "bigint unsigned",
				"lease_owner_id":    "varchar(128) character set ascii collate ascii_bin",
				"lease_expires_at":  "datetime(6)",
				"delivered_at":      "datetime(6)",
				"last_error_code":   "varchar(64) character set ascii collate ascii_bin",
			},
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			parsed, err := schema.Parse(testCase.value, &sync.Map{}, schema.NamingStrategy{})
			if err != nil {
				t.Fatalf("parse GORM schema: %v", err)
			}
			for column, wantType := range testCase.wantTypes {
				field := parsed.FieldsByDBName[column]
				if field == nil {
					t.Errorf("GORM schema missing column %q", column)
					continue
				}
				if got := strings.ToLower(string(field.DataType)); got != wantType {
					t.Errorf("GORM column %s type = %q, want %q", column, got, wantType)
				}
			}
			for _, column := range []string{"created_at", "updated_at"} {
				field := parsed.FieldsByDBName[column]
				if field == nil {
					continue
				}
				if field.AutoCreateTime != 0 || field.AutoUpdateTime != 0 {
					t.Errorf("GORM column %s must use database-authoritative time, autoCreate=%d autoUpdate=%d", column, field.AutoCreateTime, field.AutoUpdateTime)
				}
				if !strings.EqualFold(field.DefaultValue, "CURRENT_TIMESTAMP(6)") ||
					strings.Contains(strings.ToLower(field.DefaultValue), "on update") {
					t.Errorf("GORM column %s default = %q, want CURRENT_TIMESTAMP(6) without ON UPDATE", column, field.DefaultValue)
				}
			}
		})
	}

	deliveryField, ok := reflect.TypeOf(CommerceOutbox{}).FieldByName("DeliveryAttempts")
	if !ok || deliveryField.Type.Kind() != reflect.Int64 {
		t.Fatalf("CommerceOutbox.DeliveryAttempts type = %v, want int64", deliveryField.Type)
	}
}
