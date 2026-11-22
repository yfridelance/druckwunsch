package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"druckwunsch/internal/ingest"
	"druckwunsch/internal/store"
)

// ---- login ----

func (s *Server) loginPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "login", "Anmelden", map[string]any{
		"Next":     r.URL.Query().Get("next"),
		"DevMode":  s.Auth.DevMode(),
		"OIDCName": s.OIDCName,
		"Denied":   r.URL.Query().Get("denied") != "",
	})
}

// ---- overview ----

type overviewData struct {
	Items     []store.Item
	Lists     []store.List
	Labels    []store.Label
	Filter    store.Filter
	Platforms map[string]string
	Counts    map[string]int
	Users     []store.User // printers: filter by requester
	Drafts    int          // own drafts not yet released
}

func (s *Server) overview(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.Filter{Status: q.Get("status"), Platform: q.Get("platform"), Query: q.Get("q"), Sort: q.Get("sort")}
	f.ListID, _ = strconv.ParseInt(q.Get("list"), 10, 64)
	f.LabelID, _ = strconv.ParseInt(q.Get("label"), 10, 64)
	f.RequesterID, _ = strconv.ParseInt(q.Get("by"), 10, 64)
	if _, set := q["status"]; !set {
		f.Status = "offen"
	}
	u := me(r)
	items, err := s.Store.Items(r.Context(), u.ID, f)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	lists, err := s.Store.ListsFor(r.Context(), u.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	labels, _ := s.Store.Labels(r.Context())
	counts, _ := s.Store.StatusCounts(r.Context(), u.ID, f)
	d := overviewData{
		Items: items, Lists: lists, Labels: labels, Filter: f, Platforms: ingest.PlatformNames, Counts: counts,
	}
	if u.IsPrinter() {
		d.Users, _ = s.Store.Users(r.Context())
	}
	if all, err := s.Store.StatusCounts(r.Context(), u.ID, store.Filter{ListID: f.ListID}); err == nil {
		d.Drafts = all["entwurf"]
	}
	s.render(w, r, "overview", "Übersicht", d)
}

// releaseDrafts hands all drafts (of one list or all lists) to the printer.
func (s *Server) releaseDrafts(w http.ResponseWriter, r *http.Request) {
	n, err := s.Store.ReleaseDrafts(r.Context(), me(r).ID, formInt(r, "list"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	back := "/"
	if l := formInt(r, "list"); l != 0 {
		back = "/?list=" + strconv.FormatInt(l, 10)
	}
	redirect(w, r, back, strconv.Itoa(n)+" freigegeben")
}

// ---- add by URL / share target ----

type addData struct {
	URL, Title, Note, RequestedBy string
	RequesterID                   int64
	Lists                         []store.List
	Users                         []store.User // printers: wish on behalf of
	ListID                        int64
	Error                         string
}

func (s *Server) editableLists(ctx context.Context, userID int64) ([]store.List, error) {
	all, err := s.Store.ListsFor(ctx, userID)
	if err != nil {
		return nil, err
	}
	var out []store.List
	for _, l := range all {
		if l.Role.CanEdit() {
			out = append(out, l)
		}
	}
	return out, nil
}

func (s *Server) addForm(w http.ResponseWriter, r *http.Request) {
	s.showAdd(w, r, addData{URL: r.URL.Query().Get("url")})
}

func (s *Server) showAdd(w http.ResponseWriter, r *http.Request, d addData) {
	var err error
	if d.Lists, err = s.editableLists(r.Context(), me(r).ID); err != nil {
		s.fail(w, r, err)
		return
	}
	if d.ListID == 0 {
		d.ListID, _ = strconv.ParseInt(r.URL.Query().Get("list"), 10, 64)
	}
	if me(r).IsPrinter() {
		d.Users, _ = s.Store.Users(r.Context())
	}
	s.render(w, r, "add", "Hinzufügen", d)
}

// shareTarget receives links shared from a phone (PWA share_target or an
// iOS shortcut). The URL often arrives inside "text".
func (s *Server) shareTarget(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	link := q.Get("url")
	if link == "" {
		link = ingest.ExtractURL(q.Get("text"))
	}
	if link == "" {
		link = ingest.ExtractURL(q.Get("title"))
	}
	note := strings.TrimSpace(strings.Replace(q.Get("text"), link, "", 1))
	s.showAdd(w, r, addData{URL: link, Note: note})
}

func (s *Server) addSubmit(w http.ResponseWriter, r *http.Request) {
	u := me(r)
	d := addData{
		URL: formStr(r, "url", 2000), Title: formStr(r, "title", 300), Note: formStr(r, "note", 4000),
		RequestedBy: formStr(r, "requested_by", 200), RequesterID: formInt(r, "requester"), ListID: formInt(r, "list"),
	}
	ref, err := ingest.Parse(d.URL)
	if err != nil {
		d.Error = err.Error()
		s.showAdd(w, r, d)
		return
	}
	model := store.Model{Platform: ref.Platform, SourceID: ref.SourceID, URL: ref.URL, Title: d.Title}
	// Fetch metadata for new models and for known ones still without metadata
	// (MakerWorld through its API, others through OpenGraph).
	needFetch := false
	if knownID, err := s.Store.ModelByURL(r.Context(), ref.URL); errors.Is(err, store.ErrNotFound) {
		needFetch = true
	} else if err == nil {
		known, err := s.Store.Model(r.Context(), knownID)
		needFetch = err == nil && !known.HasMetadata()
	}
	if needFetch {
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		fetched, err := ingest.FetchModel(ctx, s.Fetcher, ref)
		cancel()
		if err == nil {
			model = fetched
			if d.Title != "" {
				model.Title = d.Title
			}
		} else {
			slog.Info("Metadaten nicht verfügbar", "url", ref.URL, "err", err)
		}
	}
	modelID, err := s.Store.UpsertModel(r.Context(), model)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	itemID, err := s.createItem(r, u.ID, modelID, d.ListID, d.Note, ref.ProfileID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if model.MetadataStatus == "ok" {
		s.cacheImages(modelID)
	}
	redirect(w, r, "/items/"+strconv.FormatInt(itemID, 10), "Hinzugefügt")
}

// createItem reads the shared wish fields (requester, quantity, color,
// profile, release) from the form.
func (s *Server) createItem(r *http.Request, userID, modelID, listID int64, note, sourceProfileID string) (int64, error) {
	if listID == 0 {
		var err error
		if listID, err = s.Store.InboxID(r.Context(), userID); err != nil {
			return 0, err
		}
	}
	in := store.ItemInput{
		ModelID: modelID, ListID: listID, Note: note,
		RequesterID: formInt(r, "requester"), RequestedBy: formStr(r, "requested_by", 200),
		Release:  r.FormValue("release") == "1",
		Quantity: int(formInt(r, "quantity")), Color: formStr(r, "color", 200),
	}
	if pid := formInt(r, "profile"); pid != 0 {
		in.ProfileID = &pid
	} else if sourceProfileID != "" {
		if m, err := s.Store.Model(r.Context(), modelID); err == nil {
			for _, p := range m.Profiles {
				if p.SourceProfileID == sourceProfileID {
					id := p.ID
					in.ProfileID = &id
				}
			}
		}
	}
	return s.Store.CreateItem(r.Context(), userID, in)
}

// ---- bookmarklet ingest ----

func (s *Server) ingestLanding(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "ingest_landing", "Übernehmen", nil)
}

// ingestReceive stores a bookmarklet payload temporarily and returns a token.
// It is public (see Handler), so it is size-limited and only accepts JSON
// that parses into a model.
func (s *Server) ingestReceive(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		http.Error(w, "JSON erwartet", http.StatusUnsupportedMediaType)
		return
	}
	if n, _ := s.Store.CountPending(r.Context()); n > 200 {
		http.Error(w, "zu viele offene Übernahmen", http.StatusTooManyRequests)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, ingest.MaxPayload))
	if err != nil {
		http.Error(w, "Daten zu gross", http.StatusRequestEntityTooLarge)
		return
	}
	var p ingest.Payload
	if err := json.Unmarshal(body, &p); err != nil {
		http.Error(w, "ungültige Daten", http.StatusBadRequest)
		return
	}
	if _, _, err := ingest.FromPayload(p); err != nil {
		if _, perr := ingest.Parse(p.URL); perr != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	token, err := s.Store.PutPending(r.Context(), string(body))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"next": "/ingest/" + token})
}

type ingestData struct {
	Token      string
	Model      store.Model
	Ref        ingest.Ref
	Warning    string
	Lists      []store.List
	Users      []store.User
	Existing   []store.Item
	ExistingID int64
}

func (s *Server) loadPending(r *http.Request, token string) (store.Model, ingest.Ref, string, error) {
	raw, err := s.Store.Pending(r.Context(), token)
	if err != nil {
		return store.Model{}, ingest.Ref{}, "", err
	}
	var p ingest.Payload
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return store.Model{}, ingest.Ref{}, "", err
	}
	m, ref, perr := ingest.FromPayload(p)
	warn := ""
	if perr != nil {
		warn = perr.Error()
		m = store.Model{Platform: ref.Platform, SourceID: ref.SourceID, URL: ref.URL, Title: p.OG.Title}
	} else if ref.Platform == "makerworld" && len(m.Profiles) == 0 {
		warn = "Keine Druckprofile gefunden – nur Basisdaten übernommen."
	}
	return m, ref, warn, nil
}

func (s *Server) ingestConfirm(w http.ResponseWriter, r *http.Request) {
	token := urlParam(r, "token")
	m, ref, warn, err := s.loadPending(r, token)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	u := me(r)
	lists, err := s.editableLists(r.Context(), u.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d := ingestData{Token: token, Model: m, Ref: ref, Warning: warn, Lists: lists}
	if u.IsPrinter() {
		d.Users, _ = s.Store.Users(r.Context())
	}
	if id, err := s.Store.ModelByURL(r.Context(), ref.URL); err == nil {
		d.ExistingID = id
		d.Existing, _ = s.Store.ItemsForModel(r.Context(), u.ID, id)
	}
	s.render(w, r, "ingest", "Übernehmen", d)
}

func (s *Server) ingestSave(w http.ResponseWriter, r *http.Request) {
	token := urlParam(r, "token")
	m, ref, _, err := s.loadPending(r, token)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	u := me(r)
	modelID, err := s.Store.UpsertModel(r.Context(), m)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	_ = s.Store.DeletePending(r.Context(), token)
	s.cacheImages(modelID)

	if r.FormValue("mode") == "update" {
		// only refresh metadata of an existing entry
		if itemID := formInt(r, "item"); itemID != 0 {
			redirect(w, r, "/items/"+strconv.FormatInt(itemID, 10), "Metadaten aktualisiert")
			return
		}
		redirect(w, r, "/", "Metadaten aktualisiert")
		return
	}
	sourceProfile := ref.ProfileID
	if _, ok := r.PostForm["profile_source"]; ok {
		sourceProfile = r.PostForm.Get("profile_source")
	}
	itemID, err := s.createItem(r, u.ID, modelID, formInt(r, "list"), formStr(r, "note", 4000), sourceProfile)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	redirect(w, r, "/items/"+strconv.FormatInt(itemID, 10), "Hinzugefügt")
}

// ---- item detail & actions ----

type itemData struct {
	Item     store.Item
	Model    store.Model
	Chosen   *store.Profile
	Comments []store.Comment
	Labels   []store.Label
	Lists    []store.List
	Others   []store.Item
	Users    []store.User
	Origin   string
}

func (s *Server) itemPage(w http.ResponseWriter, r *http.Request) {
	u := me(r)
	it, err := s.Store.Item(r.Context(), u.ID, idParam(r, "id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	m, err := s.Store.Model(r.Context(), it.ModelID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d := itemData{Item: it, Model: m, Origin: s.base.Scheme + "://" + s.base.Host}
	for i := range m.Profiles {
		if it.ChosenProfileID != nil && m.Profiles[i].ID == *it.ChosenProfileID {
			d.Chosen = &m.Profiles[i]
		}
	}
	d.Comments, _ = s.Store.Comments(r.Context(), it.ID)
	d.Labels, _ = s.Store.Labels(r.Context())
	d.Lists, _ = s.editableLists(r.Context(), u.ID)
	if it.CanSetRequester() {
		d.Users, _ = s.Store.Users(r.Context())
	}
	others, _ := s.Store.ItemsForModel(r.Context(), u.ID, it.ModelID)
	for _, o := range others {
		if o.ID != it.ID {
			d.Others = append(d.Others, o)
		}
	}
	title := it.Title
	s.render(w, r, "item", title, d)
}

func (s *Server) itemURL(r *http.Request) string { return "/items/" + urlParam(r, "id") }

func (s *Server) itemStatus(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.SetStatus(r.Context(), me(r).ID, idParam(r, "id"), r.FormValue("status")); err != nil {
		s.fail(w, r, err)
		return
	}
	back := r.FormValue("back")
	if back == "overview" {
		ref := r.Header.Get("Referer")
		if u, err := url.Parse(ref); err == nil && u.Path == "/" {
			http.Redirect(w, r, u.RequestURI(), http.StatusSeeOther)
			return
		}
		redirect(w, r, "/", "")
		return
	}
	redirect(w, r, s.itemURL(r), "")
}

func (s *Server) itemEdit(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	_, setReq := r.PostForm["requester"]
	err := s.Store.UpdateItem(r.Context(), me(r).ID, idParam(r, "id"), store.ItemUpdate{
		Note: formStr(r, "note", 4000), Quantity: int(formInt(r, "quantity")),
		Color: formStr(r, "color", 200), Priority: int(formInt(r, "priority")),
		SetRequester: setReq, RequesterID: formInt(r, "requester"), RequestedBy: formStr(r, "requested_by", 200),
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	redirect(w, r, s.itemURL(r), "Gespeichert")
}

func (s *Server) itemTitle(w http.ResponseWriter, r *http.Request) {
	it, err := s.Store.Item(r.Context(), me(r).ID, idParam(r, "id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !it.CanEdit() {
		s.fail(w, r, store.ErrForbidden)
		return
	}
	if title := formStr(r, "title", 300); title != "" {
		if _, err := s.Store.UpsertModel(r.Context(), store.Model{URL: it.URL, Platform: it.Platform, Title: title}); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	redirect(w, r, s.itemURL(r), "Gespeichert")
}

func (s *Server) itemProfile(w http.ResponseWriter, r *http.Request) {
	var pid *int64
	if v := formInt(r, "profile"); v != 0 {
		pid = &v
	}
	if err := s.Store.ChooseProfile(r.Context(), me(r).ID, idParam(r, "id"), pid); err != nil {
		s.fail(w, r, err)
		return
	}
	redirect(w, r, s.itemURL(r)+"#profile", "Profil gewählt")
}

func (s *Server) itemLabels(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	var ids []int64
	for _, v := range r.Form["label"] {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil {
			ids = append(ids, id)
		}
	}
	if err := s.Store.SetItemLabels(r.Context(), me(r).ID, idParam(r, "id"), ids); err != nil {
		s.fail(w, r, err)
		return
	}
	redirect(w, r, s.itemURL(r), "Labels gespeichert")
}

func (s *Server) itemComment(w http.ResponseWriter, r *http.Request) {
	if text := formStr(r, "text", 4000); text != "" {
		if err := s.Store.AddComment(r.Context(), me(r).ID, idParam(r, "id"), text); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	redirect(w, r, s.itemURL(r)+"#comments", "")
}

func (s *Server) itemMove(w http.ResponseWriter, r *http.Request) {
	newID, err := s.Store.MoveItem(r.Context(), me(r).ID, idParam(r, "id"), formInt(r, "list"), r.FormValue("copy") == "1")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	msg := "Verschoben"
	if r.FormValue("copy") == "1" {
		msg = "Kopiert"
	}
	redirect(w, r, "/items/"+strconv.FormatInt(newID, 10), msg)
}

func (s *Server) itemDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.DeleteItem(r.Context(), me(r).ID, idParam(r, "id")); err != nil {
		s.fail(w, r, err)
		return
	}
	redirect(w, r, "/", "Gelöscht")
}

// itemRefresh fetches the model metadata again (MakerWorld through its API,
// other platforms through OpenGraph).
func (s *Server) itemRefresh(w http.ResponseWriter, r *http.Request) {
	it, err := s.Store.Item(r.Context(), me(r).ID, idParam(r, "id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	ref, err := ingest.Parse(it.URL)
	if err != nil {
		redirect(w, r, s.itemURL(r), "Ungültige URL")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	model, err := ingest.FetchModel(ctx, s.Fetcher, ref)
	if err != nil {
		msg := "Abruf fehlgeschlagen: " + err.Error()
		if ref.Platform == "makerworld" {
			msg += " – bitte das Lesezeichen auf der Modellseite verwenden"
		}
		redirect(w, r, s.itemURL(r), msg)
		return
	}
	id, err := s.Store.UpsertModel(r.Context(), model)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.cacheImages(id)
	redirect(w, r, s.itemURL(r), "Metadaten aktualisiert")
}

// ---- lists ----

func (s *Server) listsPage(w http.ResponseWriter, r *http.Request) {
	lists, err := s.Store.ListsFor(r.Context(), me(r).ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, "lists", "Listen", lists)
}

func (s *Server) listCreate(w http.ResponseWriter, r *http.Request) {
	name := formStr(r, "name", 100)
	if name == "" {
		redirect(w, r, "/lists", "Name fehlt")
		return
	}
	id, err := s.Store.CreateList(r.Context(), me(r).ID, name)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	redirect(w, r, "/lists/"+strconv.FormatInt(id, 10), "Liste erstellt")
}

type listData struct {
	List    store.List
	Members []store.Member
	Users   []store.User
}

func (s *Server) listPage(w http.ResponseWriter, r *http.Request) {
	u := me(r)
	l, err := s.Store.ListFor(r.Context(), u.ID, idParam(r, "id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d := listData{List: l}
	d.Members, _ = s.Store.Members(r.Context(), l.ID)
	all, _ := s.Store.Users(r.Context())
	isMember := map[int64]bool{l.OwnerID: true}
	for _, m := range d.Members {
		isMember[m.User.ID] = true
	}
	for _, x := range all {
		if !isMember[x.ID] {
			d.Users = append(d.Users, x)
		}
	}
	s.render(w, r, "list", l.Name, d)
}

func (s *Server) listRename(w http.ResponseWriter, r *http.Request) {
	if name := formStr(r, "name", 100); name != "" {
		if err := s.Store.RenameList(r.Context(), me(r).ID, idParam(r, "id"), name); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	redirect(w, r, "/lists/"+urlParam(r, "id"), "Gespeichert")
}

func (s *Server) listShare(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.ShareList(r.Context(), me(r).ID, idParam(r, "id"), formInt(r, "user"), store.Role(r.FormValue("role"))); err != nil {
		s.fail(w, r, err)
		return
	}
	redirect(w, r, "/lists/"+urlParam(r, "id"), "Geteilt")
}

func (s *Server) listUnshare(w http.ResponseWriter, r *http.Request) {
	u := me(r)
	member := formInt(r, "user")
	if err := s.Store.Unshare(r.Context(), u.ID, idParam(r, "id"), member); err != nil {
		s.fail(w, r, err)
		return
	}
	if member == u.ID {
		redirect(w, r, "/lists", "Liste verlassen")
		return
	}
	redirect(w, r, "/lists/"+urlParam(r, "id"), "Freigabe entfernt")
}

func (s *Server) listDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.DeleteList(r.Context(), me(r).ID, idParam(r, "id")); err != nil {
		s.fail(w, r, err)
		return
	}
	redirect(w, r, "/lists", "Liste gelöscht")
}

// ---- settings & labels ----

func (s *Server) settingsPage(w http.ResponseWriter, r *http.Request) {
	labels, _ := s.Store.Labels(r.Context())
	s.render(w, r, "settings", "Einstellungen", map[string]any{
		"Labels":      labels,
		"Bookmarklet": bookmarklet(s.base.Scheme + "://" + s.base.Host),
		"ShareURL":    s.base.Scheme + "://" + s.base.Host + "/share?url=",
	})
}

func (s *Server) labelCreate(w http.ResponseWriter, r *http.Request) {
	if !me(r).IsPrinter() {
		s.fail(w, r, store.ErrForbidden)
		return
	}
	name := formStr(r, "name", 50)
	if name == "" {
		redirect(w, r, "/settings", "Name fehlt")
		return
	}
	if err := s.Store.CreateLabel(r.Context(), name, formStr(r, "color", 9)); err != nil {
		s.fail(w, r, err)
		return
	}
	redirect(w, r, "/settings#labels", "Label gespeichert")
}

func (s *Server) labelDelete(w http.ResponseWriter, r *http.Request) {
	if !me(r).IsPrinter() {
		s.fail(w, r, store.ErrForbidden)
		return
	}
	if err := s.Store.DeleteLabel(r.Context(), idParam(r, "id")); err != nil {
		s.fail(w, r, err)
		return
	}
	redirect(w, r, "/settings#labels", "Label gelöscht")
}

func (s *Server) manifest(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/manifest+json")
	json.NewEncoder(w).Encode(map[string]any{
		"name": "Druckwunsch", "short_name": "Druckwunsch", "start_url": "/", "scope": "/",
		"display": "standalone", "background_color": "#13171f", "theme_color": "#0b7d6e",
		"icons": []map[string]string{
			{"src": "/static/icon.svg", "sizes": "any", "type": "image/svg+xml", "purpose": "any"},
			{"src": "/static/icon-192.png", "sizes": "192x192", "type": "image/png"},
			{"src": "/static/icon-512.png", "sizes": "512x512", "type": "image/png"},
		},
		"share_target": map[string]any{
			"action": "/share", "method": "GET",
			"params": map[string]string{"title": "title", "text": "text", "url": "url"},
		},
	})
}
