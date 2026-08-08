package utils

import (
	"errors"
	"testing"
	"time"

	jwt "github.com/golang-jwt/jwt/v4"

	"server/model/system/request"
)

func TestParseTokenAcceptsOnlyHS256(t *testing.T) {
	parser := &JWT{SigningKey: []byte("test-signing-key")}
	claims := request.CustomClaims{StandardClaims: jwt.StandardClaims{
		ExpiresAt: time.Now().Add(time.Minute).Unix(),
	}}

	hs256 := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	hs256Raw, err := hs256.SignedString(parser.SigningKey)
	if err != nil {
		t.Fatalf("sign HS256: %v", err)
	}
	if _, err := parser.ParseToken(hs256Raw); err != nil {
		t.Fatalf("HS256 token rejected: %v", err)
	}

	hs512 := jwt.NewWithClaims(jwt.SigningMethodHS512, claims)
	hs512Raw, err := hs512.SignedString(parser.SigningKey)
	if err != nil {
		t.Fatalf("sign HS512: %v", err)
	}
	if _, err := parser.ParseToken(hs512Raw); !errors.Is(err, TokenInvalid) {
		t.Fatalf("HS512 error = %v, want TokenInvalid", err)
	}
}
