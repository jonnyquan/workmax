//go:build desktop

package desktop

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"gorm.io/gorm"
)

// Local accounts: named identities for the signed-out local route.
//
// The isolation already existed — every local-history query scopes by uid —
// but there was exactly one anonymous local uid, so one machine meant one
// undifferentiated local workspace. Accounts name those uids and let people
// who share a machine keep separate conversations, files, and RAG indexes.
//
// This is identity and separation, NOT protection: the database is plaintext
// on disk, so a password here would be security theater. Anyone who can read
// the file can read every account's data; encryption is a separate decision.
//
// Cloud sign-in (远程授权) is unchanged and orthogonal: a signed-in session
// uses the real cloud uid; the active local account governs only the
// signed-out local route.

// LocalAccount is one named local identity as the renderer sees it.
type LocalAccount struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	UID        uint64 `json:"uid,string"`
	Active     bool   `json:"active"`
	CreatedAt  string `json:"created_at"`
	LastUsedAt string `json:"last_used_at"`
}

// localAccountUID derives the identity's uid from its row id. The FIRST
// account lands exactly on the reserved single-user uid, so everything
// created before accounts existed belongs to it with no data migration.
// AUTOINCREMENT means ids are never reused, so a uid can never be re-issued
// to a different person.
func localAccountUID(id int64) uint64 {
	return localSingleUserUID + uint64(id-1)
}

const (
	defaultLocalAccountName = "Local"
	maxLocalAccountName     = 64
	maxLocalAccounts        = 32
)

var (
	errLocalAccountName     = errors.New("local account: invalid name")
	errLocalAccountTaken    = errors.New("local account: name already exists")
	errLocalAccountLimit    = errors.New("local account: too many accounts")
	errLocalAccountNotFound = errors.New("local account: not found")
)

func normalizeLocalAccountName(value string) (string, error) {
	name := strings.TrimSpace(value)
	if name == "" || !utf8.ValidString(name) || utf8.RuneCountInString(name) > maxLocalAccountName ||
		strings.ContainsAny(name, "\x00\n\r\t") {
		return "", errLocalAccountName
	}
	return name, nil
}

// ensureDefaultLocalAccount guarantees the table always has an active row.
// Called from the read path: the default account is how the pre-accounts
// world keeps working — its uid IS the old single-user uid.
func ensureDefaultLocalAccount(db *gorm.DB) error {
	var count int64
	if err := db.Raw(`SELECT COUNT(*) FROM w_desktop_local_account`).Row().Scan(&count); err != nil {
		return fmt.Errorf("local accounts: count: %w", err)
	}
	if count > 0 {
		return nil
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	return db.Exec(
		`INSERT INTO w_desktop_local_account (name, is_active, created_at, last_used_at) VALUES (?, 1, ?, ?)`,
		defaultLocalAccountName, now, now,
	).Error
}

// listLocalAccounts returns every account, active first then by id.
func listLocalAccounts(db *gorm.DB) ([]LocalAccount, error) {
	if err := ensureDefaultLocalAccount(db); err != nil {
		return nil, err
	}
	rows, err := db.Raw(
		`SELECT id, name, is_active, created_at, last_used_at
		   FROM w_desktop_local_account ORDER BY is_active DESC, id ASC`,
	).Rows()
	if err != nil {
		return nil, fmt.Errorf("local accounts: list: %w", err)
	}
	defer rows.Close()
	out := []LocalAccount{}
	for rows.Next() {
		var (
			a      LocalAccount
			active int
		)
		if err := rows.Scan(&a.ID, &a.Name, &active, &a.CreatedAt, &a.LastUsedAt); err != nil {
			return nil, fmt.Errorf("local accounts: scan: %w", err)
		}
		a.Active = active == 1
		a.UID = localAccountUID(a.ID)
		out = append(out, a)
	}
	return out, rows.Err()
}

// createLocalAccount adds a named identity. It does NOT activate it — showing
// up in the switcher and taking over the workspace are different consents.
func createLocalAccount(db *gorm.DB, rawName string) (LocalAccount, error) {
	name, err := normalizeLocalAccountName(rawName)
	if err != nil {
		return LocalAccount{}, err
	}
	if err := ensureDefaultLocalAccount(db); err != nil {
		return LocalAccount{}, err
	}
	var count int64
	if err := db.Raw(`SELECT COUNT(*) FROM w_desktop_local_account`).Row().Scan(&count); err != nil {
		return LocalAccount{}, fmt.Errorf("local accounts: count: %w", err)
	}
	if count >= maxLocalAccounts {
		return LocalAccount{}, errLocalAccountLimit
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res := db.Exec(
		`INSERT INTO w_desktop_local_account (name, is_active, created_at, last_used_at) VALUES (?, 0, ?, ?)`,
		name, now, now,
	)
	if res.Error != nil {
		if strings.Contains(strings.ToLower(res.Error.Error()), "unique") {
			return LocalAccount{}, errLocalAccountTaken
		}
		return LocalAccount{}, fmt.Errorf("local accounts: insert: %w", res.Error)
	}
	var id int64
	if err := db.Raw(`SELECT id FROM w_desktop_local_account WHERE name = ?`, name).Row().Scan(&id); err != nil {
		return LocalAccount{}, fmt.Errorf("local accounts: read back: %w", err)
	}
	return LocalAccount{ID: id, Name: name, UID: localAccountUID(id), CreatedAt: now, LastUsedAt: now}, nil
}

// renameLocalAccount renames one identity. The uid does not move: the name
// is a label on the identity, not the identity itself.
func renameLocalAccount(db *gorm.DB, id int64, rawName string) (LocalAccount, error) {
	name, err := normalizeLocalAccountName(rawName)
	if err != nil {
		return LocalAccount{}, err
	}
	res := db.Exec(`UPDATE w_desktop_local_account SET name = ? WHERE id = ?`, name, id)
	if res.Error != nil {
		if strings.Contains(strings.ToLower(res.Error.Error()), "unique") {
			return LocalAccount{}, errLocalAccountTaken
		}
		return LocalAccount{}, fmt.Errorf("local accounts: rename: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return LocalAccount{}, errLocalAccountNotFound
	}
	var (
		a      LocalAccount
		active int
	)
	row := db.Raw(
		`SELECT id, name, is_active, created_at, last_used_at FROM w_desktop_local_account WHERE id = ?`, id,
	).Row()
	if err := row.Scan(&a.ID, &a.Name, &active, &a.CreatedAt, &a.LastUsedAt); err != nil {
		return LocalAccount{}, fmt.Errorf("local accounts: rename read back: %w", err)
	}
	a.Active = active == 1
	a.UID = localAccountUID(a.ID)
	return a, nil
}

// selectLocalAccount makes one account active — atomically exactly one.
func selectLocalAccount(db *gorm.DB, id int64) error {
	if err := ensureDefaultLocalAccount(db); err != nil {
		return err
	}
	return db.Transaction(func(tx *gorm.DB) error {
		var exists int64
		if err := tx.Raw(`SELECT COUNT(*) FROM w_desktop_local_account WHERE id = ?`, id).Row().Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return errLocalAccountNotFound
		}
		if err := tx.Exec(`UPDATE w_desktop_local_account SET is_active = 0 WHERE is_active = 1`).Error; err != nil {
			return err
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		return tx.Exec(
			`UPDATE w_desktop_local_account SET is_active = 1, last_used_at = ? WHERE id = ?`,
			now, id,
		).Error
	})
}

// activeLocalAccountUID is what the local route runs as. Any failure falls
// back to the reserved single-user uid — the pre-accounts behavior — because
// an account bookkeeping problem must never lock a user out of their data.
func activeLocalAccountUID(db *gorm.DB) uint64 {
	if db == nil {
		return localSingleUserUID
	}
	if err := ensureDefaultLocalAccount(db); err != nil {
		return localSingleUserUID
	}
	var id int64
	if err := db.Raw(
		`SELECT id FROM w_desktop_local_account WHERE is_active = 1 ORDER BY id LIMIT 1`,
	).Row().Scan(&id); err != nil {
		return localSingleUserUID
	}
	return localAccountUID(id)
}
