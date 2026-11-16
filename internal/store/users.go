package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"
)

type User struct {
	ID    int64
	Sub   string
	Email string
	Name  string
}

// DisplayName returns the best human-readable name.
func (u User) DisplayName() string {
	if u.Name != "" {
		return u.Name
	}
	if u.Email != "" {
		return u.Email
	}
	return u.Sub
}

// UpsertUser creates or updates a user by OIDC subject and makes sure the
// user has an inbox list.
func (s *Store) UpsertUser(ctx context.Context, sub, email, name string) (User, error) {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO users (oidc_sub, email, name) VALUES (?, ?, ?)
		ON CONFLICT (oidc_sub) DO UPDATE SET email = excluded.email, name = excluded.name`,
		sub, email, name)
	if err != nil {
		return User{}, err
	}
	var u User
	if err := s.db.QueryRowContext(ctx, `SELECT id, oidc_sub, email, name FROM users WHERE oidc_sub = ?`, sub).
		Scan(&u.ID, &u.Sub, &u.Email, &u.Name); err != nil {
		return User{}, err
	}
	if _, err := s.InboxID(ctx, u.ID); err != nil {
		return User{}, err
	}
	return u, nil
}

func (s *Store) UserByID(ctx context.Context, id int64) (User, error) {
	var u User
	err := s.db.QueryRowContext(ctx, `SELECT id, oidc_sub, email, name FROM users WHERE id = ?`, id).
		Scan(&u.ID, &u.Sub, &u.Email, &u.Name)
	if errors.Is(err, sql.ErrNoRows) {
		return u, ErrNotFound
	}
	return u, err
}

func (s *Store) Users(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, oidc_sub, email, name FROM users ORDER BY name, email`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Sub, &u.Email, &u.Name); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func RandomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func (s *Store) CreateSession(ctx context.Context, userID int64, ttl time.Duration) (string, error) {
	token := RandomToken()
	_, err := s.db.ExecContext(ctx, `INSERT INTO sessions (token, user_id, expires_at) VALUES (?, ?, ?)`,
		token, userID, time.Now().Add(ttl).UTC())
	return token, err
}

func (s *Store) SessionUser(ctx context.Context, token string) (User, error) {
	var u User
	err := s.db.QueryRowContext(ctx, `
		SELECT u.id, u.oidc_sub, u.email, u.name FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token = ? AND s.expires_at > ?`, token, time.Now().UTC()).
		Scan(&u.ID, &u.Sub, &u.Email, &u.Name)
	if errors.Is(err, sql.ErrNoRows) {
		return u, ErrNotFound
	}
	return u, err
}

func (s *Store) DeleteSession(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token = ?`, token)
	return err
}

// Cleanup removes expired sessions and stale pending ingests.
func (s *Store) Cleanup(ctx context.Context) error {
	now := time.Now().UTC()
	if _, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at < ?`, now); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM pending_ingest WHERE created_at < ?`, now.Add(-time.Hour))
	return err
}
