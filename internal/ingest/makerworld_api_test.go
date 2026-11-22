package ingest

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestFetchMakerWorld(t *testing.T) {
	design, err := os.ReadFile("testdata/makerworld_design.json")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/3292116":
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Write(design)
		case "/7":
			w.Header().Set("Content-Type", "text/html")
			w.Header().Set("Cf-Mitigated", "challenge")
			w.WriteHeader(http.StatusForbidden)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	old := MakerWorldAPI
	MakerWorldAPI = srv.URL + "/"
	defer func() { MakerWorldAPI = old }()

	ref, _ := Parse("https://makerworld.com/de/models/3292116-smart-roller-blind#profileId-3735590")
	m, err := FetchModel(context.Background(), srv.Client(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if m.Title == "" || m.URL != "https://makerworld.com/models/3292116" || len(m.Profiles) == 0 || m.MetadataStatus != "ok" {
		t.Errorf("%+v", m)
	}
	if _, err := FetchMakerWorld(context.Background(), srv.Client(), Ref{Platform: "makerworld", SourceID: "7"}); err == nil {
		t.Error("expected bot-check error")
	}
	if _, err := FetchMakerWorld(context.Background(), srv.Client(), Ref{Platform: "makerworld", SourceID: "8"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
	if _, err := FetchMakerWorld(context.Background(), srv.Client(), Ref{Platform: "makerworld", SourceID: "../x"}); err == nil {
		t.Error("expected id error")
	}
}

// Live check against the real API: DRUCKWUNSCH_LIVE=1 go test -run Live ./internal/ingest
func TestFetchMakerWorldLive(t *testing.T) {
	if os.Getenv("DRUCKWUNSCH_LIVE") == "" {
		t.Skip("DRUCKWUNSCH_LIVE not set")
	}
	ref, _ := Parse("https://makerworld.com/de/models/3292116")
	m, err := FetchMakerWorld(context.Background(), SafeClient(0), ref)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%q von %q, %d Bilder, %d Profile", m.Title, m.Designer, len(m.Images), len(m.Profiles))
	if len(m.Profiles) == 0 {
		t.Error("no profiles")
	}
}
