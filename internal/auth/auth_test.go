package auth

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestPasswordHashAndCheck(t *testing.T) {
	a := New("secret")
	hash, err := a.HashPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if hash == "correct horse" {
		t.Fatal("password stored in clear text")
	}
	if !a.CheckPassword(hash, "correct horse") {
		t.Error("correct password rejected")
	}
	if a.CheckPassword(hash, "wrong") {
		t.Error("wrong password accepted")
	}
}

func TestUserTokenRoundTrip(t *testing.T) {
	a := New("secret")
	tok, err := a.SignUser("u1", "a@example.com")
	if err != nil {
		t.Fatal(err)
	}
	c, err := a.ParseUser(tok)
	if err != nil {
		t.Fatal(err)
	}
	if c.UserID != "u1" || c.Email != "a@example.com" {
		t.Errorf("claims = %+v", c)
	}
}

func TestTokenSignedWithOtherSecretIsRejected(t *testing.T) {
	tok, _ := New("one").SignUser("u1", "a@example.com")
	if _, err := New("two").ParseUser(tok); err == nil {
		t.Fatal("token from a different secret was accepted")
	}
}

func TestExpiredTokenIsRejected(t *testing.T) {
	a := New("secret")
	tok, _ := a.SignConn("ws1", "agent", -time.Minute)
	if _, err := a.ParseConn(tok); err == nil {
		t.Fatal("expired token was accepted")
	}
}

func TestUnsignedTokenIsRejected(t *testing.T) {
	a := New("secret")
	tok, err := jwt.NewWithClaims(jwt.SigningMethodNone, UserClaims{UserID: "admin"}).SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.ParseUser(tok); err == nil {
		t.Fatal("alg=none token was accepted")
	}
}

func TestConnTokenCarriesWorkspaceAndRole(t *testing.T) {
	a := New("secret")
	tok, _ := a.SignConn("ws1", "forward", time.Hour)
	c, err := a.ParseConn(tok)
	if err != nil {
		t.Fatal(err)
	}
	if c.WorkspaceID != "ws1" || c.Role != "forward" {
		t.Errorf("claims = %+v", c)
	}
}

func TestUserAndConnTokensAreNotInterchangeable(t *testing.T) {
	a := New("secret")
	user, _ := a.SignUser("u1", "u@x.dev")
	conn, _ := a.SignConn("w1", "agent", time.Hour)
	if _, err := a.ParseUser(conn); err == nil {
		t.Error("connection token was accepted as a user token")
	}
	if _, err := a.ParseConn(user); err == nil {
		t.Error("user token was accepted as a connection token")
	}
	if c, err := a.ParseUser(user); err != nil || c.UserID != "u1" {
		t.Errorf("valid user token rejected: %v", err)
	}
	if c, err := a.ParseConn(conn); err != nil || c.WorkspaceID != "w1" || c.Role != "agent" {
		t.Errorf("valid connection token rejected: %v", err)
	}
}
