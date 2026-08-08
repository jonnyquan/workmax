package tools

import (
	"testing"

	"server/model"
	"server/utils/testutil"
)

// voice_preset round-trip against the SQLite test DB. The API
// handler layer is a thin GORM wrapper — these focus on the
// model + DDL wiring. The pointer-to-string Update contract is
// covered by the stronger-typed integration tests elsewhere.

func TestCharacter_VoicePresetRoundTrip(t *testing.T) {
	db := testutil.NewTestDB(t)

	ch := model.Character{
		UID:         7,
		Name:        "王总",
		RoleType:    model.CharacterRoleProtagonist,
		Status:      model.CharacterStatusActive,
		VoicePreset: "male-deep",
	}
	if err := db.Create(&ch).Error; err != nil {
		t.Fatalf("create: %v", err)
	}

	var reloaded model.Character
	if err := db.First(&reloaded, ch.Id).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.VoicePreset != "male-deep" {
		t.Errorf("voicePreset roundtrip: got %q", reloaded.VoicePreset)
	}
}

func TestCharacter_VoicePresetNullableDefault(t *testing.T) {
	// Existing rows (pre-migration) have no voice_preset. Reading
	// back should yield an empty string, which the API + UI treat as
	// "unassigned" without raising errors.
	db := testutil.NewTestDB(t)
	ch := model.Character{
		UID:      7,
		Name:     "Alice",
		RoleType: model.CharacterRoleSupporting,
		Status:   model.CharacterStatusActive,
	}
	if err := db.Create(&ch).Error; err != nil {
		t.Fatalf("create: %v", err)
	}
	var reloaded model.Character
	db.First(&reloaded, ch.Id)
	if reloaded.VoicePreset != "" {
		t.Errorf("unset voicePreset should read as empty, got %q", reloaded.VoicePreset)
	}
}

func TestCharacter_VoicePresetUpdateClears(t *testing.T) {
	// Sending voice_preset="" via the update path should clear the
	// column. Mirrors the pointer-to-string contract on
	// updateCharacterRequest.VoicePreset.
	db := testutil.NewTestDB(t)
	ch := model.Character{
		UID:         7,
		Name:        "王总",
		RoleType:    model.CharacterRoleProtagonist,
		Status:      model.CharacterStatusActive,
		VoicePreset: "male-deep",
	}
	if err := db.Create(&ch).Error; err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := db.Model(&model.Character{}).
		Where("id = ?", ch.Id).
		Update("voice_preset", "").Error; err != nil {
		t.Fatalf("clear: %v", err)
	}

	var reloaded model.Character
	db.First(&reloaded, ch.Id)
	if reloaded.VoicePreset != "" {
		t.Errorf("expected cleared voicePreset, got %q", reloaded.VoicePreset)
	}
}
