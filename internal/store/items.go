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

// Statuses in workflow order. "entwurf" is only visible to the requester's
// lists until it is released; the printer's queue starts at "neu".
var Statuses = []Status{
	{"entwurf", "Entwurf"},
	{"neu", "Neu"},
	{"zu_drucken", "Zu drucken"},
	{"in_druck", "In Druck"},
	{"gedruckt", "Gedruckt"},
	{"verworfen", "Verworfen"},
}

// Workflow are the printer's steps (without draft and discarded).
var Workflow = Statuses[1:5]

// OpenStatuses are the statuses counted as "offen".
var OpenStatuses = []string{"entwurf", "neu", "zu_drucken", "in_druck"}

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
	ListOwner       string
	Role            Role // the viewer's role on the item's list ("" = no list access)
	ViewerPrinter   bool // the viewer has the printer role
	AddedBy         string
	RequesterID     *int64
	RequestedBy     string // display name of whoever wished it
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

// CanManage: may move or delete the item (list editors).
func (i Item) CanManage() bool { return i.Role.CanEdit() }

// CanEdit: may change details, labels and profile. Printers may do so on
// every released wish.
func (i Item) CanEdit() bool {
	return i.Role.CanEdit() || (i.ViewerPrinter && i.Status != "entwurf")
}

// CanPrint: may move the item through the print workflow.
func (i Item) CanPrint() bool { return i.ViewerPrinter && i.Status != "entwurf" }

// CanRelease: may hand a draft over to the printer.
func (i Item) CanRelease() bool { return i.Role.CanEdit() && i.Status == "entwurf" }

// CanWithdraw: may take a released wish back as long as nobody started on it.
func (i Item) CanWithdraw() bool { return i.Role.CanEdit() && i.Status == "neu" }

// CanSetRequester: only printers record wishes on behalf of others.
func (i Item) CanSetRequester() bool { return i.ViewerPrinter && i.CanEdit() }

type ItemInput struct {
	ModelID     int64
	ListID      int64
	RequesterID int64  // 0 = the creating user
	RequestedBy string // free-text name instead of a user (printers only)
	Release     bool   // requesters: release right away instead of keeping a draft
	Note        string
	Quantity    int
	Color       string
	ProfileID   *int64
}

type Filter struct {
	ListID      int64
	RequesterID int64
	Status      string
	LabelID     int64
	Platform    string
	Query       string
	Sort        string // "neu" (default), "prio", "titel"
}

// itemFrom joins everything an item row needs and restricts it to what the
// viewer may see: items in their lists, wishes made for them, and, for
// printers, every released wish. Parameters: viewer id ×3.
const itemFrom = `
	FROM items i
	LEFT JOIN (` + accessibleListsSQL + `) a ON a.list_id = i.list_id
	JOIN users v ON v.id = ?
	JOIN lists l ON l.id = i.list_id
	JOIN users lo ON lo.id = l.owner_id
	JOIN models m ON m.id = i.model_id
	JOIN users u ON u.id = i.added_by
	LEFT JOIN users r ON r.id = i.requester_id
	WHERE (a.role IS NOT NULL OR i.requester_id = v.id OR (v.role = 'drucker' AND i.status <> 'entwurf'))`

const itemSelect = `
	SELECT i.id, i.model_id, i.list_id, l.name, COALESCE(NULLIF(lo.name,''), lo.email), COALESCE(a.role, ''),
	       v.role = 'drucker', COALESCE(NULLIF(u.name,''), u.email),
	       i.requester_id, COALESCE(NULLIF(r.name,''), r.email, i.requested_by),
	       i.status, i.chosen_profile_id, i.quantity, i.color, i.priority, i.note,
	       i.printed_at, i.created_at,
	       m.platform, COALESCE(NULLIF(m.title,''), m.url), m.url, m.designer, m.cover_image, m.metadata_status` + itemFrom

func viewerArgs(userID int64) []any { return []any{userID, userID, userID} }

func scanItem(sc interface{ Scan(...any) error }) (Item, error) {
	var it Item
	var prof, req sql.NullInt64
	var printed sql.NullTime
	err := sc.Scan(&it.ID, &it.ModelID, &it.ListID, &it.ListName, &it.ListOwner, &it.Role,
		&it.ViewerPrinter, &it.AddedBy, &req, &it.RequestedBy,
		&it.Status, &prof, &it.Quantity, &it.Color, &it.Priority, &it.Note,
		&printed, &it.CreatedAt,
		&it.Platform, &it.Title, &it.URL, &it.Designer, &it.CoverImage, &it.MetadataStatus)
	if prof.Valid {
		it.ChosenProfileID = &prof.Int64
	}
	if req.Valid {
		it.RequesterID = &req.Int64
	}
	if printed.Valid {
		it.PrintedAt = &printed.Time
	}
	return it, err
}

func (s *Store) queryItems(ctx context.Context, q string, args ...any) ([]Item, error) {
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
	return out, rows.Err()
}

func filterSQL(f Filter) (string, []any) {
	var q strings.Builder
	var args []any
	if f.ListID != 0 {
		q.WriteString(` AND i.list_id = ?`)
		args = append(args, f.ListID)
	}
	if f.RequesterID != 0 {
		q.WriteString(` AND i.requester_id = ?`)
		args = append(args, f.RequesterID)
	}
	if f.LabelID != 0 {
		q.WriteString(` AND EXISTS (SELECT 1 FROM item_labels il WHERE il.item_id = i.id AND il.label_id = ?)`)
		args = append(args, f.LabelID)
	}
	if f.Platform != "" {
		q.WriteString(` AND m.platform = ?`)
		args = append(args, f.Platform)
	}
	if t := strings.TrimSpace(f.Query); t != "" {
		like := "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(t) + "%"
		q.WriteString(` AND (m.title LIKE ? ESCAPE '\' OR m.designer LIKE ? ESCAPE '\' OR i.note LIKE ? ESCAPE '\'
		            OR COALESCE(r.name, i.requested_by) LIKE ? ESCAPE '\' OR m.url LIKE ? ESCAPE '\')`)
		args = append(args, like, like, like, like, like)
	}
	return q.String(), args
}

// Items returns all items visible to the user, filtered.
func (s *Store) Items(ctx context.Context, userID int64, f Filter) ([]Item, error) {
	where, fargs := filterSQL(f)
	q := itemSelect + where
	args := append(viewerArgs(userID), fargs...)
	if f.Status == "offen" {
		q += ` AND i.status IN ('` + strings.Join(OpenStatuses, "','") + `')`
	} else if f.Status != "" {
		q += ` AND i.status = ?`
		args = append(args, f.Status)
	}
	switch f.Sort {
	case "prio":
		q += ` ORDER BY i.priority DESC, i.created_at DESC`
	case "titel":
		q += ` ORDER BY m.title COLLATE NOCASE`
	default:
		q += ` ORDER BY i.created_at DESC, i.id DESC`
	}
	out, err := s.queryItems(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	return out, s.attachLabels(ctx, out)
}

// StatusCounts counts the visible items per status, with the same filters
// as Items (the status filter itself is ignored).
func (s *Store) StatusCounts(ctx context.Context, userID int64, f Filter) (map[string]int, error) {
	where, fargs := filterSQL(f)
	rows, err := s.db.QueryContext(ctx, `SELECT i.status, COUNT(*)`+itemFrom+where+` GROUP BY i.status`,
		append(viewerArgs(userID), fargs...)...)
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
		for _, o := range OpenStatuses {
			if k == o {
				out["offen"] += n
			}
		}
	}
	return out, rows.Err()
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
	it, err := scanItem(s.db.QueryRowContext(ctx, itemSelect+` AND i.id = ?`, append(viewerArgs(userID), itemID)...))
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
	return s.queryItems(ctx, itemSelect+` AND i.model_id = ? ORDER BY i.created_at`, append(viewerArgs(userID), modelID)...)
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

func (s *Store) isPrinter(ctx context.Context, userID int64) bool {
	u, err := s.UserByID(ctx, userID)
	return err == nil && u.IsPrinter()
}

// resolveRequester decides who a wish is for. Requesters always wish for
// themselves; printers may name another user or a free-text name.
func (s *Store) resolveRequester(ctx context.Context, userID int64, printer bool, reqID int64, reqName string) (any, string, error) {
	if !printer {
		return userID, "", nil
	}
	if reqName = strings.TrimSpace(reqName); reqName != "" {
		return nil, reqName, nil
	}
	if reqID == 0 {
		return userID, "", nil
	}
	if _, err := s.UserByID(ctx, reqID); err != nil {
		return nil, "", err
	}
	return reqID, "", nil
}

func (s *Store) CreateItem(ctx context.Context, userID int64, in ItemInput) (int64, error) {
	if err := s.requireListEdit(ctx, userID, in.ListID); err != nil {
		return 0, err
	}
	printer := s.isPrinter(ctx, userID)
	reqID, reqName, err := s.resolveRequester(ctx, userID, printer, in.RequesterID, in.RequestedBy)
	if err != nil {
		return 0, err
	}
	// Printers' own entries go straight into the queue.
	status := "entwurf"
	if printer || in.Release {
		status = "neu"
	}
	if in.Quantity < 1 {
		in.Quantity = 1
	}
	if in.ProfileID != nil && !s.profileBelongs(ctx, *in.ProfileID, in.ModelID) {
		in.ProfileID = nil
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO items (model_id, list_id, added_by, requester_id, requested_by, status, note, quantity, color, chosen_profile_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		in.ModelID, in.ListID, userID, reqID, reqName, status, in.Note, in.Quantity, in.Color, in.ProfileID)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err == nil && status == "neu" && !printer {
		err = s.addComment(ctx, id, userID, "Freigegeben", true)
	}
	return id, err
}

func (s *Store) profileBelongs(ctx context.Context, profileID, modelID int64) bool {
	var n int
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM profiles WHERE id = ? AND model_id = ?`, profileID, modelID).Scan(&n)
	return n > 0
}

// SetStatus changes the status. Requesters (list editors) may only release
// a draft or withdraw it again while it is "neu"; printers run the rest of
// the workflow on released wishes.
func (s *Store) SetStatus(ctx context.Context, userID, itemID int64, status string) error {
	it, err := s.Item(ctx, userID, itemID)
	if err != nil {
		return err
	}
	if !validStatus(status) {
		return fmt.Errorf("ungültiger Status %q", status)
	}
	if it.Status == status {
		return nil
	}
	msg := "Status: " + StatusLabel(it.Status) + " → " + StatusLabel(status)
	switch {
	case it.Status == "entwurf" && status == "neu" && it.CanRelease():
		msg = "Freigegeben"
	case it.Status == "neu" && status == "entwurf" && it.CanWithdraw():
		msg = "Zurückgezogen"
	case it.CanPrint() && status != "entwurf":
	default:
		return ErrForbidden
	}
	var printed any
	if status == "gedruckt" {
		printed = time.Now().UTC()
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE items SET status = ?, printed_at = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		status, printed, itemID); err != nil {
		return err
	}
	return s.addComment(ctx, itemID, userID, msg, true)
}

// ReleaseDrafts releases all drafts in the user's editable lists (or in one
// of them) and returns how many were released.
func (s *Store) ReleaseDrafts(ctx context.Context, userID, listID int64) (int, error) {
	q := `SELECT i.id FROM items i JOIN (` + accessibleListsSQL + `) a ON a.list_id = i.list_id
		WHERE i.status = 'entwurf' AND a.role IN ('owner', 'editor')`
	args := []any{userID, userID}
	if listID != 0 {
		q += ` AND i.list_id = ?`
		args = append(args, listID)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return 0, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		if err := s.SetStatus(ctx, userID, id, "neu"); err != nil {
			return 0, err
		}
	}
	return len(ids), nil
}

type ItemUpdate struct {
	Note     string
	Quantity int
	Color    string
	Priority int

	SetRequester bool // printers only
	RequesterID  int64
	RequestedBy  string
}

func (s *Store) UpdateItem(ctx context.Context, userID, itemID int64, u ItemUpdate) error {
	it, err := s.requireItemEdit(ctx, userID, itemID)
	if err != nil {
		return err
	}
	if u.Quantity < 1 {
		u.Quantity = 1
	}
	if u.Priority < -1 || u.Priority > 2 {
		u.Priority = 0
	}
	if _, err := s.db.ExecContext(ctx, `
		UPDATE items SET note=?, quantity=?, color=?, priority=?, updated_at=CURRENT_TIMESTAMP
		WHERE id=?`, u.Note, u.Quantity, u.Color, u.Priority, itemID); err != nil {
		return err
	}
	if !u.SetRequester || !it.CanSetRequester() {
		return nil
	}
	reqID, reqName, err := s.resolveRequester(ctx, userID, true, u.RequesterID, u.RequestedBy)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE items SET requester_id = ?, requested_by = ? WHERE id = ?`, reqID, reqName, itemID)
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
	if !copy && !it.CanManage() {
		return 0, ErrForbidden
	}
	if err := s.requireListEdit(ctx, userID, targetList); err != nil {
		return 0, err
	}
	if !copy {
		_, err := s.db.ExecContext(ctx, `UPDATE items SET list_id = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, targetList, itemID)
		return itemID, err
	}
	in := ItemInput{
		ModelID: it.ModelID, ListID: targetList, Note: it.Note,
		Quantity: it.Quantity, Color: it.Color, ProfileID: it.ChosenProfileID,
	}
	if it.RequesterID != nil {
		in.RequesterID = *it.RequesterID
	} else {
		in.RequestedBy = it.RequestedBy
	}
	return s.CreateItem(ctx, userID, in)
}

func (s *Store) DeleteItem(ctx context.Context, userID, itemID int64) error {
	it, err := s.Item(ctx, userID, itemID)
	if err != nil {
		return err
	}
	if !it.CanManage() {
		return ErrForbidden
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM items WHERE id = ?`, itemID)
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
