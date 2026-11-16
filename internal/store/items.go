package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

type Status struct {
	Key   string
	Label string
}

// Statuses in workflow order.
var Statuses = []Status{
	{"neu", "Neu"},
	{"zu_drucken", "Zu drucken"},
	{"in_druck", "In Druck"},
	{"gedruckt", "Gedruckt"},
	{"verworfen", "Verworfen"},
}

func StatusLabel(key string) string {
	for _, s := range Statuses {
		if s.Key == key {
			return s.Label
		}
	}
	return key
}

func validStatus(key string) bool {
	for _, s := range Statuses {
		if s.Key == key {
			return true
		}
	}
	return false
}

type Label struct {
	ID    int64
	Name  string
	Color string
}

type Item struct {
	ID              int64
	ModelID         int64
	ListID          int64
	ListName        string
	Role            Role
	AddedBy         string
	RequestedBy     string
	Status          string
	ChosenProfileID *int64
	Quantity        int
	Color           string
	Priority        int
	Note            string
	PrintedAt       *time.Time
	CreatedAt       time.Time
	Labels          []Label

	// joined model summary
	Platform       string
	Title          string
	URL            string
	Designer       string
	CoverImage     string
	MetadataStatus string
}

func (i Item) StatusLabel() string { return StatusLabel(i.Status) }
func (i Item) CanEdit() bool       { return i.Role.CanEdit() }

type ItemInput struct {
	ModelID     int64
	ListID      int64
	RequestedBy string
	Note        string
	Quantity    int
	Color       string
	ProfileID   *int64
}

type Filter struct {
	ListID   int64
	Status   string
	LabelID  int64
	Platform string
	Query    string
	Sort     string // "neu" (default), "prio", "titel"
}

const itemSelect = `
	SELECT i.id, i.model_id, i.list_id, l.name, a.role, COALESCE(NULLIF(u.name,''), u.email),
	       i.requested_by, i.status, i.chosen_profile_id, i.quantity, i.color, i.priority, i.note,
	       i.printed_at, i.created_at,
	       m.platform, COALESCE(NULLIF(m.title,''), m.url), m.url, m.designer, m.cover_image, m.metadata_status
	FROM items i
	JOIN (` + accessibleListsSQL + `) a ON a.list_id = i.list_id
	JOIN lists l ON l.id = i.list_id
	JOIN models m ON m.id = i.model_id
	JOIN users u ON u.id = i.added_by`

func scanItem(sc interface{ Scan(...any) error }) (Item, error) {
	var it Item
	var prof sql.NullInt64
	var printed sql.NullTime
	err := sc.Scan(&it.ID, &it.ModelID, &it.ListID, &it.ListName, &it.Role, &it.AddedBy,
		&it.RequestedBy, &it.Status, &prof, &it.Quantity, &it.Color, &it.Priority, &it.Note,
		&printed, &it.CreatedAt,
		&it.Platform, &it.Title, &it.URL, &it.Designer, &it.CoverImage, &it.MetadataStatus)
	if prof.Valid {
		it.ChosenProfileID = &prof.Int64
	}
	if printed.Valid {
		it.PrintedAt = &printed.Time
	}
	return it, err
}

// Items returns all items visible to the user, filtered.
func (s *Store) Items(ctx context.Context, userID int64, f Filter) ([]Item, error) {
	q := itemSelect + ` WHERE 1=1`
	args := []any{userID, userID}
	if f.ListID != 0 {
		q += ` AND i.list_id = ?`
		args = append(args, f.ListID)
	}
	if f.Status == "offen" {
		q += ` AND i.status IN ('neu','zu_drucken','in_druck')`
	} else if f.Status != "" {
		q += ` AND i.status = ?`
		args = append(args, f.Status)
	}
	if f.LabelID != 0 {
		q += ` AND EXISTS (SELECT 1 FROM item_labels il WHERE il.item_id = i.id AND il.label_id = ?)`
		args = append(args, f.LabelID)
	}
	if f.Platform != "" {
		q += ` AND m.platform = ?`
		args = append(args, f.Platform)
	}
	if t := strings.TrimSpace(f.Query); t != "" {
		like := "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(t) + "%"
		q += ` AND (m.title LIKE ? ESCAPE '\' OR m.designer LIKE ? ESCAPE '\' OR i.note LIKE ? ESCAPE '\'
		            OR i.requested_by LIKE ? ESCAPE '\' OR m.url LIKE ? ESCAPE '\')`
		args = append(args, like, like, like, like, like)
	}
	switch f.Sort {
	case "prio":
		q += ` ORDER BY i.priority DESC, i.created_at DESC`
	case "titel":
		q += ` ORDER BY m.title COLLATE NOCASE`
	default:
		q += ` ORDER BY i.created_at DESC, i.id DESC`
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Item
	for rows.Next() {
		it, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, s.attachLabels(ctx, out)
}

func (s *Store) attachLabels(ctx context.Context, items []Item) error {
	if len(items) == 0 {
		return nil
	}
	idx := map[int64]int{}
	args := make([]any, len(items))
	for n, it := range items {
		idx[it.ID] = n
		args[n] = it.ID
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT il.item_id, l.id, l.name, l.color FROM item_labels il JOIN labels l ON l.id = il.label_id
		WHERE il.item_id IN (`+placeholders(len(args))+`) ORDER BY l.name`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var itemID int64
		var l Label
		if err := rows.Scan(&itemID, &l.ID, &l.Name, &l.Color); err != nil {
			return err
		}
		items[idx[itemID]].Labels = append(items[idx[itemID]].Labels, l)
	}
	return rows.Err()
}

// Item returns one item if the user may see it.
func (s *Store) Item(ctx context.Context, userID, itemID int64) (Item, error) {
	it, err := scanItem(s.db.QueryRowContext(ctx, itemSelect+` WHERE i.id = ?`, userID, userID, itemID))
	if errors.Is(err, sql.ErrNoRows) {
		return it, ErrNotFound
	}
	if err != nil {
		return it, err
	}
	items := []Item{it}
	err = s.attachLabels(ctx, items)
	return items[0], err
}

// ItemsForModel returns all visible items of a model (to show "already in list X").
func (s *Store) ItemsForModel(ctx context.Context, userID, modelID int64) ([]Item, error) {
	rows, err := s.db.QueryContext(ctx, itemSelect+` WHERE i.model_id = ? ORDER BY i.created_at`, userID, userID, modelID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Item
	for rows.Next() {
		it, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

func (s *Store) requireListEdit(ctx context.Context, userID, listID int64) error {
	l, err := s.ListFor(ctx, userID, listID)
	if err != nil {
		return err
	}
	if !l.Role.CanEdit() {
		return ErrForbidden
	}
	return nil
}

func (s *Store) requireItemEdit(ctx context.Context, userID, itemID int64) (Item, error) {
	it, err := s.Item(ctx, userID, itemID)
	if err != nil {
		return it, err
	}
	if !it.CanEdit() {
		return it, ErrForbidden
	}
	return it, nil
}

func (s *Store) CreateItem(ctx context.Context, userID int64, in ItemInput) (int64, error) {
	if err := s.requireListEdit(ctx, userID, in.ListID); err != nil {
		return 0, err
	}
	if in.Quantity < 1 {
		in.Quantity = 1
	}
	if in.ProfileID != nil && !s.profileBelongs(ctx, *in.ProfileID, in.ModelID) {
		in.ProfileID = nil
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO items (model_id, list_id, added_by, requested_by, note, quantity, color, chosen_profile_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		in.ModelID, in.ListID, userID, in.RequestedBy, in.Note, in.Quantity, in.Color, in.ProfileID)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) profileBelongs(ctx context.Context, profileID, modelID int64) bool {
	var n int
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM profiles WHERE id = ? AND model_id = ?`, profileID, modelID).Scan(&n)
	return n > 0
}

func (s *Store) SetStatus(ctx context.Context, userID, itemID int64, status string) error {
	it, err := s.requireItemEdit(ctx, userID, itemID)
	if err != nil {
		return err
	}
	if !validStatus(status) {
		return fmt.Errorf("ungültiger Status %q", status)
	}
	if it.Status == status {
		return nil
	}
	var printed any
	if status == "gedruckt" {
		printed = time.Now().UTC()
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE items SET status = ?, printed_at = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		status, printed, itemID); err != nil {
		return err
	}
	return s.addComment(ctx, itemID, userID, "Status: "+StatusLabel(it.Status)+" → "+StatusLabel(status), true)
}

type ItemUpdate struct {
	RequestedBy string
	Note        string
	Quantity    int
	Color       string
	Priority    int
}

func (s *Store) UpdateItem(ctx context.Context, userID, itemID int64, u ItemUpdate) error {
	if _, err := s.requireItemEdit(ctx, userID, itemID); err != nil {
		return err
	}
	if u.Quantity < 1 {
		u.Quantity = 1
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE items SET requested_by=?, note=?, quantity=?, color=?, priority=?, updated_at=CURRENT_TIMESTAMP
		WHERE id=?`, u.RequestedBy, u.Note, u.Quantity, u.Color, u.Priority, itemID)
	return err
}

func (s *Store) ChooseProfile(ctx context.Context, userID, itemID int64, profileID *int64) error {
	it, err := s.requireItemEdit(ctx, userID, itemID)
	if err != nil {
		return err
	}
	if profileID != nil && !s.profileBelongs(ctx, *profileID, it.ModelID) {
		return ErrNotFound
	}
	_, err = s.db.ExecContext(ctx, `UPDATE items SET chosen_profile_id = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, profileID, itemID)
	return err
}

// MoveItem moves (copy=false) or copies an item to another list.
func (s *Store) MoveItem(ctx context.Context, userID, itemID, targetList int64, copy bool) (int64, error) {
	it, err := s.Item(ctx, userID, itemID)
	if err != nil {
		return 0, err
	}
	if !copy && !it.CanEdit() {
		return 0, ErrForbidden
	}
	if err := s.requireListEdit(ctx, userID, targetList); err != nil {
		return 0, err
	}
	if !copy {
		_, err := s.db.ExecContext(ctx, `UPDATE items SET list_id = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, targetList, itemID)
		return itemID, err
	}
	return s.CreateItem(ctx, userID, ItemInput{
		ModelID: it.ModelID, ListID: targetList, RequestedBy: it.RequestedBy, Note: it.Note,
		Quantity: it.Quantity, Color: it.Color, ProfileID: it.ChosenProfileID,
	})
}

func (s *Store) DeleteItem(ctx context.Context, userID, itemID int64) error {
	if _, err := s.requireItemEdit(ctx, userID, itemID); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM items WHERE id = ?`, itemID)
	return err
}

// ---- labels ----

func (s *Store) Labels(ctx context.Context) ([]Label, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, color FROM labels ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Label
	for rows.Next() {
		var l Label
		if err := rows.Scan(&l.ID, &l.Name, &l.Color); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (s *Store) CreateLabel(ctx context.Context, name, color string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO labels (name, color) VALUES (?, ?) ON CONFLICT (name) DO UPDATE SET color = excluded.color`, name, color)
	return err
}

func (s *Store) DeleteLabel(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM labels WHERE id = ?`, id)
	return err
}

// SetItemLabels replaces the labels of an item.
func (s *Store) SetItemLabels(ctx context.Context, userID, itemID int64, labelIDs []int64) error {
	if _, err := s.requireItemEdit(ctx, userID, itemID); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM item_labels WHERE item_id = ?`, itemID); err != nil {
		return err
	}
	for _, id := range labelIDs {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO item_labels (item_id, label_id) SELECT ?, id FROM labels WHERE id = ?`, itemID, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ---- comments ----

type Comment struct {
	ID        int64
	Author    string
	Text      string
	IsSystem  bool
	CreatedAt time.Time
}

func (s *Store) addComment(ctx context.Context, itemID, userID int64, text string, system bool) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO comments (item_id, user_id, text, is_system) VALUES (?, ?, ?, ?)`,
		itemID, userID, text, system)
	return err
}

// AddComment lets anyone who can see the item comment on it (viewers included).
func (s *Store) AddComment(ctx context.Context, userID, itemID int64, text string) error {
	if _, err := s.Item(ctx, userID, itemID); err != nil {
		return err
	}
	return s.addComment(ctx, itemID, userID, text, false)
}

func (s *Store) Comments(ctx context.Context, itemID int64) ([]Comment, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT c.id, COALESCE(NULLIF(u.name,''), u.email), c.text, c.is_system, c.created_at
		FROM comments c JOIN users u ON u.id = c.user_id WHERE c.item_id = ? ORDER BY c.created_at, c.id`, itemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Comment
	for rows.Next() {
		var c Comment
		if err := rows.Scan(&c.ID, &c.Author, &c.Text, &c.IsSystem, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ---- pending bookmarklet payloads ----

func (s *Store) PutPending(ctx context.Context, payload string) (string, error) {
	token := RandomToken()
	_, err := s.db.ExecContext(ctx, `INSERT INTO pending_ingest (token, payload) VALUES (?, ?)`, token, payload)
	return token, err
}

func (s *Store) Pending(ctx context.Context, token string) (string, error) {
	var p string
	err := s.db.QueryRowContext(ctx, `SELECT payload FROM pending_ingest WHERE token = ?`, token).Scan(&p)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return p, err
}

func (s *Store) DeletePending(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM pending_ingest WHERE token = ?`, token)
	return err
}

func (s *Store) CountPending(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pending_ingest`).Scan(&n)
	return n, err
}

// StatusCounts counts the visible items per status (optionally for one list).
func (s *Store) StatusCounts(ctx context.Context, userID, listID int64) (map[string]int, error) {
	q := `SELECT i.status, COUNT(*) FROM items i JOIN (` + accessibleListsSQL + `) a ON a.list_id = i.list_id`
	args := []any{userID, userID}
	if listID != 0 {
		q += ` WHERE i.list_id = ?`
		args = append(args, listID)
	}
	rows, err := s.db.QueryContext(ctx, q+` GROUP BY i.status`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var k string
		var n int
		if err := rows.Scan(&k, &n); err != nil {
			return nil, err
		}
		out[k] = n
		out["alle"] += n
		if k == "neu" || k == "zu_drucken" || k == "in_druck" {
			out["offen"] += n
		}
	}
	return out, rows.Err()
}
