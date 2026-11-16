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
	anna, _ := s.UpsertUser(ctx, "a", "anna@x", "Anna")
	ben, _ := s.UpsertUser(ctx, "b", "ben@x", "Ben")
	carl, _ := s.UpsertUser(ctx, "c", "carl@x", "Carl")

	modelID, err := s.UpsertModel(ctx, Model{Platform: "makerworld", URL: "https://makerworld.com/models/1", Title: "Clip", MetadataStatus: "ok",
		Profiles: []Profile{{SourceProfileID: "9", Name: "PLA"}}})
	if err != nil {
		t.Fatal(err)
	}
	inbox, _ := s.InboxID(ctx, anna.ID)
	itemID, err := s.CreateItem(ctx, anna.ID, ItemInput{ModelID: modelID, ListID: inbox, RequestedBy: "Oma"})
	if err != nil {
		t.Fatal(err)
	}

	// Ben cannot see Anna's inbox yet.
	if _, err := s.Item(ctx, ben.ID, itemID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ben sees item: %v", err)
	}
	if _, err := s.CreateItem(ctx, ben.ID, ItemInput{ModelID: modelID, ListID: inbox}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ben adds to foreign list: %v", err)
	}

	// Viewer: can see and comment, not change.
	if err := s.ShareList(ctx, anna.ID, inbox, ben.ID, RoleViewer); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Item(ctx, ben.ID, itemID); err != nil {
		t.Fatal(err)
	}
	if err := s.SetStatus(ctx, ben.ID, itemID, "gedruckt"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("viewer changed status: %v", err)
	}
	if err := s.AddComment(ctx, ben.ID, itemID, "Gerne in Blau"); err != nil {
		t.Fatal(err)
	}
	// Only the owner may share further.
	if err := s.ShareList(ctx, ben.ID, inbox, carl.ID, RoleEditor); !errors.Is(err, ErrForbidden) {
		t.Fatalf("viewer shared: %v", err)
	}

	// Editor: can change status.
	if err := s.ShareList(ctx, anna.ID, inbox, ben.ID, RoleEditor); err != nil {
		t.Fatal(err)
	}
	if err := s.SetStatus(ctx, ben.ID, itemID, "gedruckt"); err != nil {
		t.Fatal(err)
	}
	it, _ := s.Item(ctx, anna.ID, itemID)
	if it.Status != "gedruckt" || it.PrintedAt == nil {
		t.Fatalf("status not set: %+v", it)
	}
	comments, _ := s.Comments(ctx, itemID)
	if len(comments) != 2 || !comments[1].IsSystem {
		t.Fatalf("comments: %+v", comments)
	}

	// Overview filters: Ben sees the item in "Alle Listen", Carl sees nothing.
	if items, _ := s.Items(ctx, ben.ID, Filter{Status: "gedruckt"}); len(items) != 1 {
		t.Fatalf("ben overview: %d", len(items))
	}
	if items, _ := s.Items(ctx, ben.ID, Filter{Status: "offen"}); len(items) != 0 {
		t.Fatalf("offen filter: %d", len(items))
	}
	if items, _ := s.Items(ctx, carl.ID, Filter{}); len(items) != 0 {
		t.Fatalf("carl sees %d items", len(items))
	}
	if items, _ := s.Items(ctx, anna.ID, Filter{Query: "oma"}); len(items) != 1 {
		t.Fatalf("search: %d", len(items))
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
