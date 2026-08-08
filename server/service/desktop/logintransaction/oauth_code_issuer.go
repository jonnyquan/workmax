package logintransaction

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	oauthservice "server/service/desktop/oauth"
)

// IssuedAuthorization is the short-lived result that an HTTP coordinator
// redirects only to the transaction's frozen loopback URI. It must never be
// returned through the general Desktop Renderer bridge.
type IssuedAuthorization struct {
	Code        string
	ExpiresAt   time.Time
	RedirectURI string
	OAuthState  string
}

type authorizationCodeGenerator interface {
	Generate(context.Context, oauthservice.GenerateInput) (oauthservice.Generated, error)
}

// OAuthCodeIssuer atomically consumes an authenticated login transaction and
// inserts the existing OAuth authorization-code row in the same DB commit.
// It intentionally reuses the current token/refresh/device-session pipeline.
type OAuthCodeIssuer struct {
	db             *gorm.DB
	newCodeService func(*gorm.DB) authorizationCodeGenerator
}

func NewOAuthCodeIssuer(db *gorm.DB) (*OAuthCodeIssuer, error) {
	if db == nil {
		return nil, fmt.Errorf("desktop login transaction code issuer: database is required")
	}
	return &OAuthCodeIssuer{
		db: db,
		newCodeService: func(tx *gorm.DB) authorizationCodeGenerator {
			return oauthservice.NewCodeService(tx)
		},
	}, nil
}

func (i *OAuthCodeIssuer) ExchangeAndIssue(
	ctx context.Context,
	in ExchangeInput,
) (IssuedAuthorization, error) {
	if ctx == nil {
		return IssuedAuthorization{}, context.Canceled
	}
	var issued IssuedAuthorization
	err := i.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		repo, err := NewGORMRepository(tx)
		if err != nil {
			return err
		}
		transactionService, err := NewService(repo, nil, nil, Options{})
		if err != nil {
			return err
		}
		grant, err := transactionService.Exchange(ctx, in)
		if err != nil {
			return err
		}
		deviceID := grant.DeviceID
		generated, err := i.newCodeService(tx).Generate(ctx, oauthservice.GenerateInput{
			ClientID:            grant.ClientID,
			UID:                 int(grant.UserID),
			DeviceID:            &deviceID,
			RedirectURI:         grant.RedirectURI,
			CodeChallenge:       grant.CodeChallenge,
			CodeChallengeMethod: grant.CodeChallengeMethod,
			Scope:               grant.Scope,
		})
		if err != nil {
			return fmt.Errorf("desktop login transaction code issuer: issue code: %w", err)
		}
		issued = IssuedAuthorization{
			Code:        generated.Code,
			ExpiresAt:   generated.ExpiresAt,
			RedirectURI: grant.RedirectURI,
			OAuthState:  grant.OAuthState,
		}
		return nil
	})
	if err != nil {
		return IssuedAuthorization{}, err
	}
	return issued, nil
}
