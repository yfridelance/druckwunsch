package store

import (
	"context"
	"database/sql"
	"errors"
)

type Role string

const (
	RoleOwner  Role = "owner"
	RoleEditor Role = "editor"
	RoleViewer Role = "viewer"
)

func (r Role) CanEdit() bool { return r == RoleOwner || r == RoleEditor }

type List struct {
	ID        int64
	OwnerID   int64
	OwnerName string
	Name      string
	IsInbox   bool
	Role      Role // role of the requesting user
	ItemCount int
}

type Member struct {
	User User
	Role Role
}

// InboxID returns (creating if needed) the user's inbox list.
func (s *Store) InboxID(ctx context.Context, userID int64) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `SELECT id FROM lists WHERE owner_id = ? AND is_inbox = 1`, userID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		res, err := s.db.ExecContext(ctx, `INSERT INTO lists (owner_id, name, is_inbox) VALUES (?, 'Meine Wünsche', 1)`, userID)
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}
	return id, err
}

// accessibleListsSQL selects (list_id, role) for every list the user may see.
const accessibleListsSQL = `
	SELECT id AS list_id, 'owner' AS role FROM lists WHERE owner_id = ?
	UNION ALL
	SELECT list_id, role FROM list_members WHERE user_id = ?`

// ListsFor returns all lists the user can access.
func (s *Store) ListsFor(ctx context.Context, userID int64) ([]List, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT l.id, l.owner_id, COALESCE(NULLIF(u.name,''), u.email), l.name, l.is_inbox, a.role,
		       (SELECT COUNT(*) FROM items i WHERE i.list_id = l.id)
		FROM (`+accessibleListsSQL+`) a
		JOIN lists l ON l.id = a.list_id
		JOIN users u ON u.id = l.owner_id
		ORDER BY a.role = 'owner' DESC, l.is_inbox DESC, l.name`, userID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []List
	for rows.Next() {
		var l List
		if err := rows.Scan(&l.ID, &l.OwnerID, &l.OwnerName, &l.Name, &l.IsInbox, &l.Role, &l.ItemCount); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// ListFor returns a single list if the user may access it.
func (s *Store) ListFor(ctx context.Context, userID, listID int64) (List, error) {
	lists, err := s.ListsFor(ctx, userID)
	if err != nil {
		return List{}, err
	}
	for _, l := range lists {
		if l.ID == listID {
			return l, nil
		}
	}
	return List{}, ErrNotFound
}

func (s *Store) CreateList(ctx context.Context, userID int64, name string) (int64, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO lists (owner_id, name) VALUES (?, ?)`, userID, name)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) requireOwner(ctx context.Context, userID, listID int64) (List, error) {
	l, err := s.ListFor(ctx, userID, listID)
	if err != nil {
		return l, err
	}
	if l.Role != RoleOwner {
		return l, ErrForbidden
	}
	return l, nil
}

func (s *Store) RenameList(ctx context.Context, userID, listID int64, name string) error {
	if _, err := s.requireOwner(ctx, userID, listID); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `UPDATE lists SET name = ? WHERE id = ?`, name, listID)
	return err
}

func (s *Store) DeleteList(ctx context.Context, userID, listID int64) error {
	l, err := s.requireOwner(ctx, userID, listID)
	if err != nil {
		return err
	}
	if l.IsInbox {
		return ErrForbidden
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM lists WHERE id = ?`, listID)
	return err
}

func (s *Store) Members(ctx context.Context, listID int64) ([]Member, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT u.id, u.oidc_sub, u.email, u.name, m.role FROM list_members m JOIN users u ON u.id = m.user_id
		WHERE m.list_id = ? ORDER BY u.name, u.email`, listID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Member
	for rows.Next() {
		var m Member
		if err := rows.Scan(&m.User.ID, &m.User.Sub, &m.User.Email, &m.User.Name, &m.Role); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ShareList adds or updates a member. Only the owner may share.
func (s *Store) ShareList(ctx context.Context, userID, listID, memberID int64, role Role) error {
	l, err := s.requireOwner(ctx, userID, listID)
	if err != nil {
		return err
	}
	if memberID == l.OwnerID || (role != RoleViewer && role != RoleEditor) {
		return ErrForbidden
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO list_members (list_id, user_id, role) VALUES (?, ?, ?)
		ON CONFLICT (list_id, user_id) DO UPDATE SET role = excluded.role`, listID, memberID, role)
	return err
}

// Unshare removes a member. The owner may remove anyone; members may leave.
func (s *Store) Unshare(ctx context.Context, userID, listID, memberID int64) error {
	if userID != memberID {
		if _, err := s.requireOwner(ctx, userID, listID); err != nil {
			return err
		}
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM list_members WHERE list_id = ? AND user_id = ?`, listID, memberID)
	return err
}
