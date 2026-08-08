package logintransaction

import (
	"context"
	"errors"
	"strings"
	"testing"

	"gorm.io/gorm"

	oauthmodel "server/model/desktop/oauth"
	oauthservice "server/service/desktop/oauth"
)

type acceptingPasswordAuthenticator struct{ userID uint }

func (a acceptingPasswordAuthenticator) AuthenticatePassword(
	context.Context, string, string,
) (Principal, error) {
	return Principal{UserID: a.userID}, nil
}

type failingCodeGenerator struct{}

func (failingCodeGenerator) Generate(
	context.Context, oauthservice.GenerateInput,
) (oauthservice.Generated, error) {
	return oauthservice.Generated{}, errors.New("injected code insert failure")
}

func authenticatedPersistentTransaction(
	t *testing.T,
) (*GORMRepository, *gorm.DB, Completion) {
	t.Helper()
	repo, db := newGORMLoginRepository(t)
	if err := db.AutoMigrate(&oauthmodel.AuthorizationCode{}); err != nil {
		t.Fatalf("migrate authorization code: %v", err)
	}
	service, err := NewService(repo, acceptingPasswordAuthenticator{userID: 42}, nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	handle, err := service.Create(context.Background(), CreateInput{
		ClientID:            oauthmodel.DesktopClientID,
		RedirectURI:         "http://127.0.0.1:49152/oauth/callback",
		OAuthState:          "MDEyMzQ1Njc4OWFiY2RlZg",
		CodeChallenge:       "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM",
		CodeChallengeMethod: "S256",
		Scope:               "workagent",
		DeviceID:            "2825400e4ecb442f7b842f022cd40d4e",
	})
	if err != nil {
		t.Fatal(err)
	}
	completion, err := service.CompletePassword(context.Background(), PasswordCompletionInput{
		TransactionID:     handle.TransactionID,
		TransactionSecret: handle.TransactionSecret,
		Email:             "person@example.com",
		Password:          "password",
	})
	if err != nil {
		t.Fatal(err)
	}
	return repo, db, completion
}

func TestOAuthCodeIssuerCommitsTransactionAndDeviceBoundCodeTogether(t *testing.T) {
	repo, db, completion := authenticatedPersistentTransaction(t)
	issuer, err := NewOAuthCodeIssuer(db)
	if err != nil {
		t.Fatal(err)
	}

	issued, err := issuer.ExchangeAndIssue(context.Background(), ExchangeInput{
		TransactionID: completion.TransactionID,
		ExchangeToken: completion.ExchangeToken,
	})
	if err != nil {
		t.Fatalf("ExchangeAndIssue: %v", err)
	}
	if issued.Code == "" || issued.RedirectURI != "http://127.0.0.1:49152/oauth/callback" || issued.OAuthState != "MDEyMzQ1Njc4OWFiY2RlZg" {
		t.Fatalf("issued = %+v", issued)
	}

	stored, err := repo.Get(context.Background(), completion.TransactionID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.State != StateExchanged {
		t.Fatalf("transaction state = %q, want exchanged", stored.State)
	}
	var code oauthmodel.AuthorizationCode
	if err := db.Where("code = ?", issued.Code).First(&code).Error; err != nil {
		t.Fatal(err)
	}
	if code.DeviceID == nil || *code.DeviceID != "2825400e4ecb442f7b842f022cd40d4e" {
		t.Fatalf("authorization code device binding = %+v", code.DeviceID)
	}
	if _, err := issuer.ExchangeAndIssue(context.Background(), ExchangeInput{
		TransactionID: completion.TransactionID,
		ExchangeToken: completion.ExchangeToken,
	}); !errors.Is(err, ErrReplay) {
		t.Fatalf("replay error = %v, want ErrReplay", err)
	}
	var count int64
	if err := db.Model(&oauthmodel.AuthorizationCode{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("authorization code count = %d, err = %v, want 1", count, err)
	}
}

func TestOAuthCodeIssuerRollsBackExchangeWhenCodeInsertFails(t *testing.T) {
	repo, db, completion := authenticatedPersistentTransaction(t)
	issuer, err := NewOAuthCodeIssuer(db)
	if err != nil {
		t.Fatal(err)
	}
	issuer.newCodeService = func(*gorm.DB) authorizationCodeGenerator { return failingCodeGenerator{} }

	_, err = issuer.ExchangeAndIssue(context.Background(), ExchangeInput{
		TransactionID: completion.TransactionID,
		ExchangeToken: completion.ExchangeToken,
	})
	if err == nil || !strings.Contains(err.Error(), "injected code insert failure") {
		t.Fatalf("error = %v", err)
	}
	stored, err := repo.Get(context.Background(), completion.TransactionID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.State != StateAuthenticated || stored.ExchangeTokenHash == ([32]byte{}) {
		t.Fatalf("failed issue did not roll transaction back: %+v", stored)
	}
}
