package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/oauth2"
)

// OIDCConfig holds the settings for a single OIDC provider.
type OIDCConfig struct {
	IssuerURL    string // e.g. https://accounts.google.com
	ClientID     string
	ClientSecret string
	RedirectURL  string // must match the redirect URI registered with the IdP
	Scopes       []string
	// Display
	ButtonText string // defaults to "Sign in with SSO"
	IconURL    string
}

// OIDCProvider wraps go-oidc and oauth2 for a single IdP.
type OIDCProvider struct {
	cfg      OIDCConfig
	provider *gooidc.Provider
	oauth    oauth2.Config
	verifier *gooidc.IDTokenVerifier
}

// NewOIDCProvider initialises the OIDC provider by fetching the IdP discovery doc.
func NewOIDCProvider(ctx context.Context, cfg OIDCConfig) (*OIDCProvider, error) {
	if cfg.IssuerURL == "" || cfg.ClientID == "" || cfg.ClientSecret == "" || cfg.RedirectURL == "" {
		return nil, errors.New("oidc: issuer_url, client_id, client_secret, and redirect_url are required")
	}

	scopes := cfg.Scopes
	if len(scopes) == 0 {
		scopes = []string{gooidc.ScopeOpenID, "profile", "email"}
	}
	// Always include openid
	hasOpenID := false
	for _, s := range scopes {
		if s == gooidc.ScopeOpenID {
			hasOpenID = true
			break
		}
	}
	if !hasOpenID {
		scopes = append([]string{gooidc.ScopeOpenID}, scopes...)
	}

	provider, err := gooidc.NewProvider(ctx, cfg.IssuerURL)
	if err != nil {
		return nil, fmt.Errorf("oidc: fetch discovery doc from %s: %w", cfg.IssuerURL, err)
	}

	oa := oauth2.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		RedirectURL:  cfg.RedirectURL,
		Endpoint:     provider.Endpoint(),
		Scopes:       scopes,
	}

	verifier := provider.Verifier(&gooidc.Config{ClientID: cfg.ClientID})

	return &OIDCProvider{
		cfg:      cfg,
		provider: provider,
		oauth:    oa,
		verifier: verifier,
	}, nil
}

// AuthCodeURL returns the IdP redirect URL. state must be an opaque CSRF token.
func (o *OIDCProvider) AuthCodeURL(state string) string {
	return o.oauth.AuthCodeURL(state, oauth2.AccessTypeOnline)
}

// Exchange exchanges the authorization code for an ID token and returns the
// user's email, display name, and group memberships. email is always lowercased.
func (o *OIDCProvider) Exchange(ctx context.Context, code string) (email, name string, groups []string, err error) {
	oaToken, err := o.oauth.Exchange(ctx, code)
	if err != nil {
		return "", "", nil, fmt.Errorf("oidc: code exchange: %w", err)
	}

	// Extract and verify the ID token from the OAuth2 token response.
	rawID, ok := oaToken.Extra("id_token").(string)
	if !ok {
		return "", "", nil, errors.New("oidc: no id_token in token response")
	}
	idToken, err := o.verifier.Verify(ctx, rawID)
	if err != nil {
		return "", "", nil, fmt.Errorf("oidc: verify id_token: %w", err)
	}

	// Merge claims: start with ID token claims, overlay with UserInfo claims.
	var claims struct {
		Email         string   `json:"email"`
		EmailVerified bool     `json:"email_verified"`
		Name          string   `json:"name"`
		GivenName     string   `json:"given_name"`
		FamilyName    string   `json:"family_name"`
		PreferredName string   `json:"preferred_username"`
		Groups        []string `json:"groups"`
	}
	if err := idToken.Claims(&claims); err != nil {
		return "", "", nil, fmt.Errorf("oidc: parse id_token claims: %w", err)
	}

	// Try UserInfo endpoint for fresher claims (some IdPs only put basics in the ID token).
	userInfo, uiErr := o.provider.UserInfo(ctx, oauth2.StaticTokenSource(oaToken))
	if uiErr == nil {
		_ = userInfo.Claims(&claims)
	}

	if claims.Email == "" {
		return "", "", nil, errors.New("oidc: no email claim in id_token or userinfo")
	}

	// Derive display name from best available claim.
	displayName := claims.Name
	if displayName == "" {
		displayName = strings.TrimSpace(claims.GivenName + " " + claims.FamilyName)
	}
	if displayName == "" {
		displayName = claims.PreferredName
	}
	if displayName == "" {
		displayName = claims.Email
	}

	return strings.ToLower(strings.TrimSpace(claims.Email)), displayName, claims.Groups, nil
}

// ButtonText returns the label to show on the login button.
func (o *OIDCProvider) ButtonText() string {
	if o.cfg.ButtonText != "" {
		return o.cfg.ButtonText
	}
	return "Sign in with SSO"
}

// IconURL returns the provider icon URL (may be empty).
func (o *OIDCProvider) IconURL() string { return o.cfg.IconURL }

// ─── CSRF state tokens ────────────────────────────────────────────────────────

type oidcStateClaims struct {
	Nonce string `json:"nonce"`
	jwt.RegisteredClaims
}

// SignOIDCState creates a short-lived signed state token for CSRF protection.
func (a *Auth) SignOIDCState(nonce string) (string, error) {
	claims := oidcStateClaims{
		Nonce: nonce,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(5 * time.Minute)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(a.secret)
}

// ParseOIDCState verifies a state token and returns the embedded nonce.
func (a *Auth) ParseOIDCState(token string) (string, error) {
	t, err := jwt.ParseWithClaims(token, &oidcStateClaims{}, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("unexpected signing method")
		}
		return a.secret, nil
	})
	if err != nil {
		return "", err
	}
	c, ok := t.Claims.(*oidcStateClaims)
	if !ok || !t.Valid {
		return "", errors.New("invalid state token")
	}
	return c.Nonce, nil
}
