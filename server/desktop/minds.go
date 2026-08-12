//go:build desktop

package desktop

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Minds (心智体): named, trainable personas over this machine's shared
// knowledge base.
//
// The anatomy is the product's, and it maps onto existing machinery rather
// than new storage:
//
//   - BRAIN (大脑) — which model the mind thinks with. model_override when
//     set, otherwise the identity's own model route (migration 0009). There
//     is deliberately no second model configuration surface: a mind inherits
//     the machine's endpoints and only names a different model on top of
//     them.
//   - CEREBELLUM (小脑) — the skills it has mastered. This version does not
//     train skills; the mind practices the agent skills this machine already
//     has, so there is no skills column here to drift away from the catalog.
//   - MEMORY (记忆体) — the knowledge chunks marked as its own. The mark is
//     a source_id prefix in the existing vec0 store ("mind:<id>:...", see
//     knowledge/indexer.go), so a mind's memory is queryable with the
//     metadata filter the store already has, and one identity's minds share
//     one knowledge base exactly as the product decision states.
//
// Identity-scoped like everything that owns user data: uid is the active
// local account's derived uid, or the connected cloud account's subject.

// Mind is one mind as the renderer sees it. ModelOverride is empty (not
// null on the wire) when the mind inherits the identity's model.
type Mind struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Description   string `json:"description"`
	RoleHint      string `json:"role_hint"`
	ModelOverride string `json:"model_override"`
	Active        bool   `json:"active"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
}

// MindPut is the create body. One required field, three optional shades of
// intent.
type MindPut struct {
	Name          string `json:"name"`
	Description   string `json:"description"`
	RoleHint      string `json:"role_hint"`
	ModelOverride string `json:"model_override"`
}

const (
	defaultMindName    = "General mind"
	defaultMindRole    = "The general-purpose mind this identity starts with."
	maxMindName        = 64
	maxMindDescription = 280
	maxMindRoleHint    = 280
	maxMindModel       = 128
	maxMinds           = 16
)

var (
	errMindName        = errors.New("mind: invalid name")
	errMindDescription = errors.New("mind: invalid description")
	errMindRoleHint    = errors.New("mind: invalid role hint")
	errMindModel       = errors.New("mind: invalid model override")
	errMindID          = errors.New("mind: invalid id")
	errMindNotFound    = errors.New("mind: not found")
	errMindLimit       = errors.New("mind: too many minds")
)

// mindIDShape is the whole vocabulary of a mind id: the prefix the knowledge
// source_id convention is built on, plus a canonical v4 uuid. Anything else
// is rejected before it can become a LIKE pattern or a path segment.
var mindIDShape = regexp.MustCompile(`^mind-[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// mindModelShape is the same alphabet the model settings accept for a model
// id, so a mind's override can never name something the route settings could
// not have named.
var mindModelShape = regexp.MustCompile(`^[A-Za-z0-9._\-:/]+$`)

func newMindID() string { return "mind-" + uuid.NewString() }

func normalizeMindName(value string) (string, error) {
	name := strings.TrimSpace(value)
	if name == "" || !utf8.ValidString(name) || utf8.RuneCountInString(name) > maxMindName ||
		strings.ContainsAny(name, "\x00\n\r\t") {
		return "", errMindName
	}
	return name, nil
}

// normalizeMindFreeText covers description and role_hint: bounded, single
// paragraph, no control characters. Both are display text the renderer
// paints as-is.
func normalizeMindFreeText(value string, maxRunes int, errInvalid error) (string, error) {
	text := strings.TrimSpace(value)
	if !utf8.ValidString(text) || utf8.RuneCountInString(text) > maxRunes ||
		strings.ContainsAny(text, "\x00\n\r\t") {
		return "", errInvalid
	}
	return text, nil
}

func normalizeMindModel(value string) (string, error) {
	model := strings.TrimSpace(value)
	if model == "" {
		return "", nil
	}
	if len(model) > maxMindModel || !mindModelShape.MatchString(model) {
		return "", errMindModel
	}
	return model, nil
}

// MindStore persists minds in w_desktop_mind (migration 0011).
type MindStore struct {
	db *gorm.DB
}

// NewMindStore wires the store to the local SQLite database.
func NewMindStore(db *gorm.DB) *MindStore {
	return &MindStore{db: db}
}

// ensureDefaultMind guarantees the identity always has one active mind, the
// way ensureDefaultLocalAccount guarantees the machine always has one active
// identity. Lazy rather than seeded by the migration: uids are minted at
// runtime, so "the default mind" can only be created at the moment an
// identity first asks.
func (s *MindStore) ensureDefaultMind(uid uint64) error {
	var count int64
	if err := s.db.Raw(`SELECT COUNT(*) FROM w_desktop_mind WHERE uid = ?`, signedMindUID(uid)).Row().Scan(&count); err != nil {
		return fmt.Errorf("minds: count: %w", err)
	}
	if count > 0 {
		return nil
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	return s.db.Exec(
		`INSERT INTO w_desktop_mind (id, uid, name, description, role_hint, model_override, is_active, created_at, updated_at)
		 VALUES (?, ?, ?, '', ?, NULL, 1, ?, ?)`,
		newMindID(), signedMindUID(uid), defaultMindName, defaultMindRole, now, now,
	).Error
}

// List returns the identity's minds, active first then by creation.
func (s *MindStore) List(uid uint64) ([]Mind, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("mind store unavailable")
	}
	if err := s.ensureDefaultMind(uid); err != nil {
		return nil, err
	}
	rows, err := s.db.Raw(
		`SELECT id, name, description, role_hint, COALESCE(model_override, ''), is_active, created_at, updated_at
		   FROM w_desktop_mind WHERE uid = ? ORDER BY is_active DESC, created_at ASC, id ASC`,
		signedMindUID(uid),
	).Rows()
	if err != nil {
		return nil, fmt.Errorf("minds: list: %w", err)
	}
	defer rows.Close()
	out := []Mind{}
	for rows.Next() {
		var (
			m      Mind
			active int
		)
		if err := rows.Scan(&m.ID, &m.Name, &m.Description, &m.RoleHint, &m.ModelOverride, &active, &m.CreatedAt, &m.UpdatedAt); err != nil {
			return nil, fmt.Errorf("minds: scan: %w", err)
		}
		m.Active = active == 1
		out = append(out, m)
	}
	return out, rows.Err()
}

// Get returns one of the identity's minds, seeding the default first so a
// status request for the only mind a fresh identity has cannot 404.
func (s *MindStore) Get(uid uint64, id string) (Mind, error) {
	if s == nil || s.db == nil {
		return Mind{}, errors.New("mind store unavailable")
	}
	if !mindIDShape.MatchString(id) {
		return Mind{}, errMindID
	}
	if err := s.ensureDefaultMind(uid); err != nil {
		return Mind{}, err
	}
	var (
		m      Mind
		active int
	)
	row := s.db.Raw(
		`SELECT id, name, description, role_hint, COALESCE(model_override, ''), is_active, created_at, updated_at
		   FROM w_desktop_mind WHERE uid = ? AND id = ?`,
		signedMindUID(uid), id,
	).Row()
	if err := row.Scan(&m.ID, &m.Name, &m.Description, &m.RoleHint, &m.ModelOverride, &active, &m.CreatedAt, &m.UpdatedAt); err != nil {
		return Mind{}, errMindNotFound
	}
	m.Active = active == 1
	return m, nil
}

// Active returns the identity's active mind.
//
// This is the accessor a turn uses, so it is deliberately forgiving in one
// direction and strict in the other: a store that is missing, a database that
// predates the mind table, or an identity with no active row all answer
// (Mind{}, false, nil) — a turn must not fail because nobody has chosen a mind
// — while a real database error is returned, because silently running an
// unscoped turn on a broken mind table would be a lie about what the answer
// was allowed to see.
func (s *MindStore) Active(uid uint64) (Mind, bool, error) {
	if s == nil || s.db == nil {
		return Mind{}, false, nil
	}
	if err := s.ensureDefaultMind(uid); err != nil {
		return Mind{}, false, err
	}
	var (
		m      Mind
		active int
	)
	row := s.db.Raw(
		`SELECT id, name, description, role_hint, COALESCE(model_override, ''), is_active, created_at, updated_at
		   FROM w_desktop_mind WHERE uid = ? AND is_active = 1 LIMIT 1`,
		signedMindUID(uid),
	).Row()
	if err := row.Scan(&m.ID, &m.Name, &m.Description, &m.RoleHint, &m.ModelOverride, &active, &m.CreatedAt, &m.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Mind{}, false, nil
		}
		return Mind{}, false, fmt.Errorf("mind store: read active mind: %w", err)
	}
	m.Active = active == 1
	return m, true, nil
}

// Create adds a mind. It does NOT activate it — same consent split as local
// accounts: appearing in the list and taking over the workspace are different
// acts.
func (s *MindStore) Create(uid uint64, in MindPut) (Mind, error) {
	if s == nil || s.db == nil {
		return Mind{}, errors.New("mind store unavailable")
	}
	name, err := normalizeMindName(in.Name)
	if err != nil {
		return Mind{}, err
	}
	description, err := normalizeMindFreeText(in.Description, maxMindDescription, errMindDescription)
	if err != nil {
		return Mind{}, err
	}
	roleHint, err := normalizeMindFreeText(in.RoleHint, maxMindRoleHint, errMindRoleHint)
	if err != nil {
		return Mind{}, err
	}
	model, err := normalizeMindModel(in.ModelOverride)
	if err != nil {
		return Mind{}, err
	}
	if err := s.ensureDefaultMind(uid); err != nil {
		return Mind{}, err
	}
	var count int64
	if err := s.db.Raw(`SELECT COUNT(*) FROM w_desktop_mind WHERE uid = ?`, signedMindUID(uid)).Row().Scan(&count); err != nil {
		return Mind{}, fmt.Errorf("minds: count: %w", err)
	}
	if count >= maxMinds {
		return Mind{}, errMindLimit
	}
	m := Mind{
		ID:            newMindID(),
		Name:          name,
		Description:   description,
		RoleHint:      roleHint,
		ModelOverride: model,
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	m.CreatedAt, m.UpdatedAt = now, now
	var modelArg any
	if model != "" {
		modelArg = model
	}
	if err := s.db.Exec(
		`INSERT INTO w_desktop_mind (id, uid, name, description, role_hint, model_override, is_active, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, 0, ?, ?)`,
		m.ID, signedMindUID(uid), name, description, roleHint, modelArg, now, now,
	).Error; err != nil {
		return Mind{}, fmt.Errorf("minds: insert: %w", err)
	}
	return m, nil
}

// Select makes one mind the identity's active one — atomically exactly one,
// the local-account pattern again.
func (s *MindStore) Select(uid uint64, id string) error {
	if s == nil || s.db == nil {
		return errors.New("mind store unavailable")
	}
	if !mindIDShape.MatchString(id) {
		return errMindID
	}
	if err := s.ensureDefaultMind(uid); err != nil {
		return err
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		var exists int64
		if err := tx.Raw(
			`SELECT COUNT(*) FROM w_desktop_mind WHERE uid = ? AND id = ?`, signedMindUID(uid), id,
		).Row().Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return errMindNotFound
		}
		if err := tx.Exec(
			`UPDATE w_desktop_mind SET is_active = 0 WHERE uid = ? AND is_active = 1`, signedMindUID(uid),
		).Error; err != nil {
			return err
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		return tx.Exec(
			`UPDATE w_desktop_mind SET is_active = 1, updated_at = ? WHERE uid = ? AND id = ?`,
			now, signedMindUID(uid), id,
		).Error
	})
}

// DecodeMindPut strictly decodes the create body: unknown fields and trailing
// content are rejected, the same rule as every other PUT/POST body here.
func DecodeMindPut(raw []byte) (MindPut, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var in MindPut
	if err := dec.Decode(&in); err != nil {
		return MindPut{}, err
	}
	if dec.More() {
		return MindPut{}, errors.New("trailing json content")
	}
	return in, nil
}

// signedMindUID mirrors knowledge's signedUID: local uids sit at 2^62 + n,
// which fits int64, and the reinterpretation is bit-for-bit so equality
// filtering stays exact either way.
func signedMindUID(uid uint64) int64 { return int64(uid) }
