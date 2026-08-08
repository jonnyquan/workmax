package identity

import (
	"context"
	"crypto/md5" // #nosec G501 -- test fixture for the legacy migration path.
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
	ormlogger "gorm.io/gorm/logger"

	logintransaction "server/service/desktop/logintransaction"
)

func newPasswordAuthenticatorTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: ormlogger.Default.LogMode(ormlogger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.Exec(`CREATE TABLE w_user (
		id INTEGER PRIMARY KEY,
		email TEXT NOT NULL UNIQUE,
		password TEXT NOT NULL,
		ban NUMERIC NOT NULL DEFAULT 0,
		updated_at DATETIME
	)`).Error; err != nil {
		t.Fatalf("create w_user: %v", err)
	}
	return db
}

func newPasswordAuthenticatorForTest(t *testing.T) (*PasswordAuthenticator, *gorm.DB) {
	t.Helper()
	db := newPasswordAuthenticatorTestDB(t)
	authenticator, err := NewPasswordAuthenticator(db)
	if err != nil {
		t.Fatalf("NewPasswordAuthenticator: %v", err)
	}
	return authenticator, db
}

func legacyMD5(password string) string {
	digest := md5.Sum([]byte(password)) // #nosec G401 -- legacy test fixture.
	return hex.EncodeToString(digest[:])
}

func TestPasswordAuthenticatorLegacySuccessUpgradesToBcrypt(t *testing.T) {
	authenticator, db := newPasswordAuthenticatorForTest(t)
	legacy := legacyMD5("correct horse battery staple")
	if err := db.Exec(`INSERT INTO w_user (id, email, password, ban) VALUES (?, ?, ?, 0)`,
		42, "person@example.com", legacy).Error; err != nil {
		t.Fatal(err)
	}

	principal, err := authenticator.AuthenticatePassword(
		context.Background(), "person@example.com", "correct horse battery staple",
	)
	if err != nil {
		t.Fatalf("AuthenticatePassword: %v", err)
	}
	if principal.UserID != 42 {
		t.Fatalf("UserID = %d, want 42", principal.UserID)
	}

	var stored string
	if err := db.Raw(`SELECT password FROM w_user WHERE id = 42`).Scan(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored == legacy || isLegacyMD5Digest(stored) {
		t.Fatalf("legacy digest was not upgraded")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(stored), []byte("correct horse battery staple")); err != nil {
		t.Fatalf("upgraded bcrypt digest does not verify: %v", err)
	}
}

func TestPasswordAuthenticatorAcceptsBcryptWithoutRewriting(t *testing.T) {
	authenticator, db := newPasswordAuthenticatorForTest(t)
	hash, err := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO w_user (id, email, password, ban) VALUES (?, ?, ?, 0)`,
		7, "bcrypt@example.com", string(hash)).Error; err != nil {
		t.Fatal(err)
	}

	principal, err := authenticator.AuthenticatePassword(context.Background(), "bcrypt@example.com", "secret")
	if err != nil || principal.UserID != 7 {
		t.Fatalf("principal = %+v, err = %v", principal, err)
	}
	var stored string
	if err := db.Raw(`SELECT password FROM w_user WHERE id = 7`).Scan(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored != string(hash) {
		t.Fatal("bcrypt digest should not be rewritten on every login")
	}
}

func TestPasswordAuthenticatorUpgradesLongLegacyPasswordWithTaggedPrehash(t *testing.T) {
	authenticator, db := newPasswordAuthenticatorForTest(t)
	password := strings.Repeat("long-password-", 8)
	if len(password) <= 72 {
		t.Fatal("test password must exceed bcrypt's direct-input limit")
	}
	if err := db.Exec(`INSERT INTO w_user (id, email, password, ban) VALUES (?, ?, ?, 0)`,
		8, "long@example.com", legacyMD5(password)).Error; err != nil {
		t.Fatal(err)
	}

	principal, err := authenticator.AuthenticatePassword(context.Background(), "long@example.com", password)
	if err != nil || principal.UserID != 8 {
		t.Fatalf("legacy long password principal=%+v err=%v", principal, err)
	}
	var stored string
	if err := db.Raw(`SELECT password FROM w_user WHERE id = 8`).Scan(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(stored, prehashedBcryptPrefix) {
		t.Fatalf("long password upgrade format = %q", stored)
	}
	principal, err = authenticator.AuthenticatePassword(context.Background(), "long@example.com", password)
	if err != nil || principal.UserID != 8 {
		t.Fatalf("tagged bcrypt reauthentication principal=%+v err=%v", principal, err)
	}
}

func TestDummyComparisonInputBoundsLongPasswords(t *testing.T) {
	short := dummyComparisonInput("short password")
	if string(short) != "short password" {
		t.Fatalf("short dummy input = %q", short)
	}
	longPassword := strings.Repeat("x", 73)
	long := dummyComparisonInput(longPassword)
	if len(long) != 32 || string(long) == longPassword {
		t.Fatalf("long dummy input length = %d, want a 32-byte digest", len(long))
	}
	hash, err := bcrypt.GenerateFromPassword([]byte("dummy"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if err := bcrypt.CompareHashAndPassword(hash, long); errors.Is(err, bcrypt.ErrPasswordTooLong) {
		t.Fatal("bounded dummy input still triggered bcrypt's fast overlength rejection")
	}
}

func TestPasswordAuthenticatorCollapsesCredentialFailures(t *testing.T) {
	authenticator, db := newPasswordAuthenticatorForTest(t)
	if err := db.Exec(`INSERT INTO w_user (id, email, password, ban) VALUES (?, ?, ?, 0), (?, ?, ?, 1)`,
		1, "active@example.com", legacyMD5("right"),
		2, "disabled@example.com", legacyMD5("right")).Error; err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name     string
		email    string
		password string
	}{
		{name: "unknown account", email: "missing@example.com", password: "right"},
		{name: "wrong password", email: "active@example.com", password: "wrong"},
		{name: "disabled account", email: "disabled@example.com", password: "right"},
		{name: "leading whitespace", email: " active@example.com", password: "right"},
		{name: "empty password", email: "active@example.com", password: ""},
		{name: "oversized password", email: "active@example.com", password: strings.Repeat("x", maxLoginPasswordBytes+1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			principal, err := authenticator.AuthenticatePassword(context.Background(), tc.email, tc.password)
			if principal.UserID != 0 {
				t.Fatalf("failure leaked principal: %+v", principal)
			}
			if !errors.Is(err, logintransaction.ErrAuthenticationFailed) {
				t.Fatalf("error = %v, want ErrAuthenticationFailed", err)
			}
		})
	}
}

func TestPasswordAuthenticatorHonorsCancelledContext(t *testing.T) {
	authenticator, _ := newPasswordAuthenticatorForTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := authenticator.AuthenticatePassword(ctx, "person@example.com", "password")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}
