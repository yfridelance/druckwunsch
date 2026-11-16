package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestPermissionsAndSharing(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	anna, _ := s.UpsertUser(ctx, "a", "anna@x", "Anna", RoleRequester)
	ben, _ := s.UpsertUser(ctx, "b", "ben@x", "Ben", RoleRequester)
	carl, _ := s.UpsertUser(ctx, "c", "carl@x", "Carl", RoleRequester)
	paul, _ := s.UpsertUser(ctx, "p", "paul@x", "Paul", RolePrinter)

	modelID, err := s.UpsertModel(ctx, Model{Platform: "makerworld", URL: "https://makerworld.com/models/1", Title: "Clip", MetadataStatus: "ok",
		Profiles: []Profile{{SourceProfileID: "9", Name: "PLA"}}})
	if err != nil {
		t.Fatal(err)
	}
	inbox, _ := s.InboxID(ctx, anna.ID)
	// Requesters always wish for themselves; a name they type is ignored.
	itemID, err := s.CreateItem(ctx, anna.ID, ItemInput{ModelID: modelID, ListID: inbox, RequestedBy: "Oma"})
	if err != nil {
		t.Fatal(err)
	}
	it, _ := s.Item(ctx, anna.ID, itemID)
	if it.Status != "entwurf" || it.RequestedBy != "Anna" || it.RequesterID == nil || *it.RequesterID != anna.ID {
		t.Fatalf("new requester item: %+v", it)
	}

	// Drafts stay private: neither Ben nor the printer sees them.
	for _, u := range []User{ben, paul} {
		if _, err := s.Item(ctx, u.ID, itemID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("%s sees draft: %v", u.Name, err)
		}
	}
	if _, err := s.CreateItem(ctx, ben.ID, ItemInput{ModelID: modelID, ListID: inbox}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ben adds to foreign list: %v", err)
	}

	// Viewer: can see and comment, not release.
	if err := s.ShareList(ctx, anna.ID, inbox, ben.ID, RoleViewer); err != nil {
		t.Fatal(err)
	}
	if err := s.SetStatus(ctx, ben.ID, itemID, "neu"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("viewer released: %v", err)
	}
	if err := s.AddComment(ctx, ben.ID, itemID, "Gerne in Blau"); err != nil {
		t.Fatal(err)
	}
	if err := s.ShareList(ctx, ben.ID, inbox, carl.ID, RoleEditor); !errors.Is(err, ErrForbidden) {
		t.Fatalf("viewer shared: %v", err)
	}

	// Editor may release but not print.
	if err := s.ShareList(ctx, anna.ID, inbox, ben.ID, RoleEditor); err != nil {
		t.Fatal(err)
	}
	if err := s.SetStatus(ctx, ben.ID, itemID, "gedruckt"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("requester printed: %v", err)
	}
	if err := s.SetStatus(ctx, ben.ID, itemID, "neu"); err != nil {
		t.Fatal(err)
	}

	// Released: the printer sees and edits it, without list access.
	it, err = s.Item(ctx, paul.ID, itemID)
	if err != nil || !it.CanPrint() || !it.CanEdit() || it.CanManage() || it.Role != "" {
		t.Fatalf("printer view: %v %+v", err, it)
	}
	// Withdraw while still "neu" hides it again.
	if err := s.SetStatus(ctx, anna.ID, itemID, "entwurf"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Item(ctx, paul.ID, itemID); !errors.Is(err, ErrNotFound) {
		t.Fatal("printer sees withdrawn wish")
	}
	if n, err := s.ReleaseDrafts(ctx, anna.ID, 0); err != nil || n != 1 {
		t.Fatalf("release all: %d %v", n, err)
	}
	if err := s.SetStatus(ctx, paul.ID, itemID, "in_druck"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetStatus(ctx, anna.ID, itemID, "entwurf"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("withdrawn while printing: %v", err)
	}
	if err := s.SetStatus(ctx, paul.ID, itemID, "gedruckt"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteItem(ctx, paul.ID, itemID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("printer deleted foreign item: %v", err)
	}
	it, _ = s.Item(ctx, anna.ID, itemID)
	if it.Status != "gedruckt" || it.PrintedAt == nil {
		t.Fatalf("status not set: %+v", it)
	}
	comments, _ := s.Comments(ctx, itemID)
	if len(comments) != 6 || comments[1].Text != "Freigegeben" || comments[2].Text != "Zurückgezogen" {
		t.Fatalf("comments: %+v", comments)
	}

	// The printer records wishes for others: a user or a free-text name.
	pInbox, _ := s.InboxID(ctx, paul.ID)
	forCarl, err := s.CreateItem(ctx, paul.ID, ItemInput{ModelID: modelID, ListID: pInbox, RequesterID: carl.ID})
	if err != nil {
		t.Fatal(err)
	}
	it, err = s.Item(ctx, carl.ID, forCarl)
	if err != nil || it.Status != "neu" || it.RequestedBy != "Carl" || it.CanEdit() {
		t.Fatalf("carl's wish: %v %+v", err, it)
	}
	if err := s.AddComment(ctx, carl.ID, forCarl, "Danke!"); err != nil {
		t.Fatal(err)
	}
	forOma, _ := s.CreateItem(ctx, paul.ID, ItemInput{ModelID: modelID, ListID: pInbox, RequestedBy: "Oma"})
	if it, _ := s.Item(ctx, paul.ID, forOma); it.RequestedBy != "Oma" || it.RequesterID != nil {
		t.Fatalf("free-text requester: %+v", it)
	}
	if err := s.UpdateItem(ctx, paul.ID, forOma, ItemUpdate{SetRequester: true, RequesterID: anna.ID}); err != nil {
		t.Fatal(err)
	}
	if it, _ := s.Item(ctx, anna.ID, forOma); it.RequestedBy != "Anna" {
		t.Fatalf("requester change: %+v", it)
	}
	// Requesters cannot reassign.
	if err := s.UpdateItem(ctx, anna.ID, itemID, ItemUpdate{SetRequester: true, RequestedBy: "Fremd"}); err != nil {
		t.Fatal(err)
	}
	if it, _ := s.Item(ctx, anna.ID, itemID); it.RequestedBy != "Anna" {
		t.Fatalf("requester reassigned: %+v", it)
	}

	// Overview: Ben sees Anna's list, Carl only the wish for him.
	if items, _ := s.Items(ctx, ben.ID, Filter{Status: "gedruckt"}); len(items) != 1 {
		t.Fatalf("ben overview: %d", len(items))
	}
	if items, _ := s.Items(ctx, carl.ID, Filter{}); len(items) != 1 || items[0].ID != forCarl {
		t.Fatalf("carl sees %d items", len(items))
	}
	if items, _ := s.Items(ctx, paul.ID, Filter{RequesterID: anna.ID}); len(items) != 2 {
		t.Fatalf("by requester: %d", len(items))
	}
	if items, _ := s.Items(ctx, paul.ID, Filter{Query: "carl"}); len(items) != 1 {
		t.Fatalf("search: %d", len(items))
	}
	if c, _ := s.StatusCounts(ctx, paul.ID, Filter{}); c["neu"] != 2 || c["gedruckt"] != 1 || c["offen"] != 2 {
		t.Fatalf("counts: %v", c)
	}

	// Inbox cannot be deleted; unsharing removes access.
	if err := s.DeleteList(ctx, anna.ID, inbox); !errors.Is(err, ErrForbidden) {
		t.Fatalf("inbox deleted: %v", err)
	}
	if err := s.Unshare(ctx, anna.ID, inbox, ben.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Item(ctx, ben.ID, itemID); !errors.Is(err, ErrNotFound) {
		t.Fatal("access after unshare")
	}
}

func TestUpsertModelKeepsMetadata(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	url := "https://makerworld.com/models/2"
	id, _ := s.UpsertModel(ctx, Model{Platform: "makerworld", URL: url, Title: "Echt", MetadataStatus: "ok",
		Profiles: []Profile{{SourceProfileID: "1"}, {SourceProfileID: "2"}}})
	// A bare re-add must not wipe metadata.
	id2, _ := s.UpsertModel(ctx, Model{Platform: "makerworld", URL: url, Title: "Manuell"})
	if id != id2 {
		t.Fatal("not deduplicated")
	}
	m, _ := s.Model(ctx, id)
	if m.Title != "Echt" || len(m.Profiles) != 2 {
		t.Fatalf("%+v", m)
	}
	// A refresh with fewer profiles drops the vanished one.
	s.UpsertModel(ctx, Model{Platform: "makerworld", URL: url, Title: "Echt 2", MetadataStatus: "ok",
		Profiles: []Profile{{SourceProfileID: "2"}}})
	m, _ = s.Model(ctx, id)
	if m.Title != "Echt 2" || len(m.Profiles) != 1 || m.Profiles[0].SourceProfileID != "2" {
		t.Fatalf("%+v", m)
	}
}
