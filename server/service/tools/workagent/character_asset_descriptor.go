package workagent

// character_asset_descriptor.go — Sprint-D 2/7 (descriptor abstraction)
// → Sprint-E 3b/8 (retargeted at platform table). Adapts the
// platform-level model.Character into the asset_library.Descriptor
// contract; reads/writes go through service/character.Default().
// Sibling of brand_asset_descriptor + director_style_asset_descriptor.

import (
	"encoding/json"
	"time"

	"server/model"
	"server/service/asset_library"
	"server/service/character"
)

// characterLibraryAsset wraps *model.Character to satisfy
// LibraryAsset.
type characterLibraryAsset struct {
	*model.Character
}

// MarshalJSON projects raw Status int8 onto the asset_library.Status
// enum string on the wire — same fix as productLibraryAsset
// (see that file for the longer rationale).
func (c characterLibraryAsset) MarshalJSON() ([]byte, error) {
	base, err := json.Marshal(c.Character)
	if err != nil {
		return nil, err
	}
	var m map[string]interface{}
	if err := json.Unmarshal(base, &m); err != nil {
		return nil, err
	}
	m["status"] = c.GetStatus()
	return json.Marshal(m)
}

func (c characterLibraryAsset) GetID() uint     { return c.Character.Id }
func (c characterLibraryAsset) GetUID() int     { return c.Character.UID }
func (c characterLibraryAsset) GetName() string { return c.Character.Name }
func (c characterLibraryAsset) GetSlug() string { return c.Character.Slug }

// GetStatus projects (Confirmed + DeletedAt) onto the
// asset_library.Status string enum.
func (c characterLibraryAsset) GetStatus() asset_library.Status {
	if c.Character.DeletedAt != nil {
		return asset_library.StatusArchived
	}
	if !c.Character.Confirmed {
		return asset_library.StatusDraft
	}
	return asset_library.StatusConfirmed
}

func (c characterLibraryAsset) GetConfirmed() bool         { return c.Character.Confirmed }
func (c characterLibraryAsset) GetConfirmedAt() *time.Time { return c.Character.ConfirmedAt }
func (c characterLibraryAsset) GetDeletedAt() *time.Time   { return c.Character.DeletedAt }
func (c characterLibraryAsset) GetUpdatedAt() time.Time    { return c.Character.UpdatedAt }
func (c characterLibraryAsset) TableName() string          { return c.Character.TableName() }

// WrapCharacter exposes the adapter for callers that already have
// a *model.Character in hand.
func WrapCharacter(c *model.Character) asset_library.LibraryAsset {
	if c == nil {
		return nil
	}
	return characterLibraryAsset{c}
}

// characterDescriptor implements asset_library.Descriptor for the
// character library. Stateless.
type characterDescriptor struct{}

func (characterDescriptor) Kind() asset_library.AssetKind { return asset_library.AssetKindCharacter }
func (characterDescriptor) IndexLabel() string            { return "characters" }
func (characterDescriptor) URLPrefix() string             { return "character-assets" }

func (characterDescriptor) NewEmpty() asset_library.LibraryAsset {
	return characterLibraryAsset{&model.Character{}}
}

func (characterDescriptor) LoadByID(id, uid uint) (asset_library.LibraryAsset, error) {
	c, err := character.Default().LoadByIDForOwner(id, uid)
	if err != nil {
		return nil, err
	}
	if c == nil {
		return nil, nil
	}
	return characterLibraryAsset{c}, nil
}

func (characterDescriptor) LoadLatestActive(uid uint) (asset_library.LibraryAsset, error) {
	c, err := character.Default().FindLatestActiveForOwner(uid)
	if err != nil {
		return nil, err
	}
	if c == nil {
		return nil, nil
	}
	return characterLibraryAsset{c}, nil
}

func (characterDescriptor) LoadLatest(uid uint) (asset_library.LibraryAsset, error) {
	c, err := character.Default().FindLatestForOwner(uid)
	if err != nil {
		return nil, err
	}
	if c == nil {
		return nil, nil
	}
	return characterLibraryAsset{c}, nil
}

func (characterDescriptor) List(uid uint, limit, offset int) ([]asset_library.LibraryAsset, error) {
	rows, err := character.Default().ListForOwner(uid, limit, offset)
	if err != nil {
		return nil, err
	}
	out := make([]asset_library.LibraryAsset, 0, len(rows))
	for i := range rows {
		out = append(out, characterLibraryAsset{&rows[i]})
	}
	return out, nil
}

func (characterDescriptor) Search(uid uint, query string, limit int) ([]asset_library.LibraryAsset, error) {
	rows, err := character.Default().SearchByOwner(uid, query, limit)
	if err != nil {
		return nil, err
	}
	out := make([]asset_library.LibraryAsset, 0, len(rows))
	for i := range rows {
		out = append(out, characterLibraryAsset{&rows[i]})
	}
	return out, nil
}

func (characterDescriptor) SearchByProject(uid, projectID uint, query string, limit int) ([]asset_library.LibraryAsset, error) {
	rows, err := character.Default().SearchByOwnerProject(uid, projectID, query, limit)
	if err != nil {
		return nil, err
	}
	out := make([]asset_library.LibraryAsset, 0, len(rows))
	for i := range rows {
		out = append(out, characterLibraryAsset{&rows[i]})
	}
	return out, nil
}

func (characterDescriptor) ListByProject(uid, projectID uint, limit, offset int) ([]asset_library.LibraryAsset, error) {
	rows, err := character.Default().ListByOwnerProject(uid, projectID, limit, offset)
	if err != nil {
		return nil, err
	}
	out := make([]asset_library.LibraryAsset, 0, len(rows))
	for i := range rows {
		out = append(out, characterLibraryAsset{&rows[i]})
	}
	return out, nil
}

func (characterDescriptor) MarkConfirmed(id, uid uint) error {
	return character.Default().MarkConfirmed(id, uid)
}

func (characterDescriptor) SoftDelete(id, uid uint) error {
	return character.Default().SoftDelete(id, uid)
}

func (characterDescriptor) Restore(id, uid uint) error {
	return character.Default().Restore(id, uid)
}

// FormatXML — type-asserts back to the concrete platform model and
// delegates to formatCharacterContextXML in preflight.go.
func (characterDescriptor) FormatXML(asset asset_library.LibraryAsset) string {
	if asset == nil {
		return ""
	}
	wrapped, ok := asset.(characterLibraryAsset)
	if !ok {
		return ""
	}
	return formatCharacterContextXML(wrapped.Character)
}

// Summarise projects model.Character into the kind-agnostic
// Summary wire shape. Type-specific hints (role / gender / ageRange
// / hasReference / canonicalImagePath) ride in Extras.
//
// Field mapping (Sprint-E):
//   - role → RoleType (platform)         ← was Role (workagent)
//   - canonicalImagePath → AvatarImageURL ← was CanonicalImagePath
//   - hasReference: true when AvatarImageURL non-empty (the
//     simple platform predicate; relational w_global_character_reference
//     join is Phase 5 work).
func (characterDescriptor) Summarise(asset asset_library.LibraryAsset) asset_library.Summary {
	if asset == nil {
		return asset_library.Summary{}
	}
	wrapped, ok := asset.(characterLibraryAsset)
	if !ok {
		return asset_library.Summary{}
	}
	c := wrapped.Character
	return asset_library.Summary{
		ID:         c.Id,
		UID:        c.UID,
		Name:       c.Name,
		Slug:       c.Slug,
		Status:     wrapped.GetStatus(),
		SourceKind: asset_library.SourceKind(c.SourceKind),
		Confirmed:  c.Confirmed,
		CreatedAt:  c.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		UpdatedAt:  c.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
		Extras: map[string]interface{}{
			"role":               c.RoleType,
			"gender":             c.Gender,
			"ageRange":           c.AgeRange,
			"hasReference":       c.AvatarImageURL != "",
			"canonicalImagePath": c.AvatarImageURL,
		},
	}
}

func init() {
	asset_library.Default().Register(characterDescriptor{})
}
