// Package auth implements OIDC login and cookie sessions.
package auth

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"druckwunsch/internal/store"
)

const (
	sessionCookie = "dw_session"
	flowCookie    = "dw_oidc"
	sessionTTL    = 30 * 24 * time.Hour
)

type Config struct {
	BaseURL      string
	Issuer       string
	ClientID     string
	ClientSecret string
	DevLogin     bool // only for local development without an IdP
}

type Auth struct {
	cfg      Config
	store    *store.Store
	provider *oidc.Provider
	verifier *oidc.IDTokenVerifier
	oauth    oauth2.Config
	secure   bool
}

type ctxKey struct{}

func New(ctx context.Context, cfg Config, st *store.Store) (*Auth, error) {
	// Accept the discovery URL as well as the plain issuer.
	cfg.Issuer = strings.TrimSuffix(strings.TrimSuffix(cfg.Issuer, "/.well-known/openid-configuration"), "/")
	a := &Auth{cfg: cfg, store: st, secure: strings.HasPrefix(cfg.BaseURL, "https://")}
	if cfg.Issuer == "" {
		if !cfg.DevLogin {
			return nil, errors.New("DW_OIDC_ISSUER fehlt (oder DW_DEV_LOGIN=true für lokale Tests)")
		}
		slog.Warn("OIDC nicht konfiguriert – Entwickler-Login aktiv, NICHT produktiv verwenden")
		return a, nil
	}
	var err error
	// The IdP may start after us; retry discovery for a while.
	for attempt := 0; attempt < 10; attempt++ {
		a.provider, err = oidc.NewProvider(ctx, cfg.Issuer)
		if err == nil {
			break
		}
		slog.Warn("OIDC-Discovery fehlgeschlagen, neuer Versuch", "err", err)
		time.Sleep(3 * time.Second)
	}
	if err != nil {
		return nil, err
	}
	a.verifier = a.provider.Verifier(&oidc.Config{ClientID: cfg.ClientID})
	a.oauth = oauth2.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		Endpoint:     a.provider.Endpoint(),
		RedirectURL:  strings.TrimSuffix(cfg.BaseURL, "/") + "/auth/callback",
		Scopes:       []string{oidc.ScopeOpenID, "profile", "email"},
	}
	return a, nil
}

// DevMode reports whether the developer login is active.
func (a *Auth) DevMode() bool { return a.provider == nil }

// User returns the logged-in user from the request context.
func User(ctx context.Context) (store.User, bool) {
	u, ok := ctx.Value(ctxKey{}).(store.User)
	return u, ok
}

// Middleware attaches the session user (if any) to the request context.
func (a *Auth) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(sessionCookie); err == nil {
			if u, err := a.store.SessionUser(r.Context(), c.Value); err == nil {
				r = r.WithContext(context.WithValue(r.Context(), ctxKey{}, u))
			}
		}
		next.ServeHTTP(w, r)
	})
}

// Require redirects anonymous users to the login page.
func (a *Auth) Require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := User(r.Context()); !ok {
			if r.Method != http.MethodGet {
				http.Error(w, "nicht angemeldet", http.StatusUnauthorized)
				return
			}
			http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func safeNext(next string) string {
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.HasPrefix(next, "/\\") {
		return "/"
	}
	return next
}

// Start begins the OIDC flow (or the dev login).
func (a *Auth) Start(w http.ResponseWriter, r *http.Request) {
	next := safeNext(r.URL.Query().Get("next"))
	if a.DevMode() {
		name := strings.TrimSpace(r.URL.Query().Get("dev_user"))
		if name == "" {
			http.Error(w, "dev_user fehlt", http.StatusBadRequest)
			return
		}
		u, err := a.store.UpsertUser(r.Context(), "dev:"+name, name+"@dev.local", name)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		a.login(w, r, u, next)
		return
	}
	state, nonce, verifier := store.RandomToken(), store.RandomToken(), oauth2.GenerateVerifier()
	http.SetCookie(w, &http.Cookie{
		Name: flowCookie, Value: url.Values{"s": {state}, "n": {nonce}, "v": {verifier}, "next": {next}}.Encode(),
		Path: "/auth/", MaxAge: 600, HttpOnly: true, Secure: a.secure, SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, a.oauth.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)), http.StatusFound)
}

// Callback completes the OIDC flow.
func (a *Auth) Callback(w http.ResponseWriter, r *http.Request) {
	if a.DevMode() {
		http.NotFound(w, r)
		return
	}
	c, err := r.Cookie(flowCookie)
	if err != nil {
		http.Error(w, "Login abgelaufen – bitte erneut versuchen", http.StatusBadRequest)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: flowCookie, Path: "/auth/", MaxAge: -1})
	flow, _ := url.ParseQuery(c.Value)
	if e := r.URL.Query().Get("error"); e != "" {
		http.Error(w, "Login abgelehnt: "+e, http.StatusForbidden)
		return
	}
	if r.URL.Query().Get("state") != flow.Get("s") || flow.Get("s") == "" {
		http.Error(w, "ungültiger state", http.StatusBadRequest)
		return
	}
	tok, err := a.oauth.Exchange(r.Context(), r.URL.Query().Get("code"), oauth2.VerifierOption(flow.Get("v")))
	if err != nil {
		slog.Error("OIDC token exchange", "err", err)
		http.Error(w, "Login fehlgeschlagen", http.StatusBadGateway)
		return
	}
	raw, ok := tok.Extra("id_token").(string)
	if !ok {
		http.Error(w, "kein id_token erhalten", http.StatusBadGateway)
		return
	}
	idt, err := a.verifier.Verify(r.Context(), raw)
	if err != nil || idt.Nonce != flow.Get("n") {
		slog.Error("OIDC id_token", "err", err)
		http.Error(w, "ungültiges id_token", http.StatusBadGateway)
		return
	}
	var claims struct {
		Email             string `json:"email"`
		Name              string `json:"name"`
		PreferredUsername string `json:"preferred_username"`
	}
	_ = idt.Claims(&claims)
	if claims.Name == "" {
		claims.Name = claims.PreferredUsername
	}
	u, err := a.store.UpsertUser(r.Context(), idt.Issuer+"|"+idt.Subject, claims.Email, claims.Name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.login(w, r, u, safeNext(flow.Get("next")))
}

func (a *Auth) login(w http.ResponseWriter, r *http.Request, u store.User, next string) {
	token, err := a.store.CreateSession(r.Context(), u.ID, sessionTTL)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/", MaxAge: int(sessionTTL.Seconds()),
		HttpOnly: true, Secure: a.secure, SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, next, http.StatusSeeOther)
}

func (a *Auth) Logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		_ = a.store.DeleteSession(r.Context(), c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Path: "/", MaxAge: -1})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}
