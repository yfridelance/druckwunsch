package web

import (
	"context"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"druckwunsch/internal/auth"
	"druckwunsch/internal/ingest"
	"druckwunsch/internal/store"
)

type client struct {
	t    *testing.T
	base string
	c    *http.Client
}

func (c *client) do(method, path, ctype, body string) (int, string, string) {
	c.t.Helper()
	req, _ := http.NewRequest(method, c.base+path, strings.NewReader(body))
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	if method == http.MethodPost {
		req.Header.Set("Origin", c.base)
	}
	resp, err := c.c.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Request.URL.Path
}

func (c *client) get(path string) (int, string, string) { return c.do(http.MethodGet, path, "", "") }
func (c *client) post(path string, form url.Values) (int, string, string) {
	return c.do(http.MethodPost, path, "application/x-www-form-urlencoded", form.Encode())
}

func setup(t *testing.T) (*client, *httptest.Server) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ts := httptest.NewUnstartedServer(nil)
	base := "http://" + ts.Listener.Addr().String()
	a, err := auth.New(context.Background(), auth.Config{BaseURL: base, DevLogin: true}, st)
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{BaseURL: base, Store: st, Auth: a, Fetcher: ingest.SafeClient(0),
		Images: &ingest.ImageCache{Dir: filepath.Join(dir, "img"), Client: ingest.SafeClient(0)}}
	h, err := srv.Handler()
	if err != nil {
		t.Fatal(err)
	}
	ts.Config.Handler = h
	ts.Start()
	t.Cleanup(ts.Close)
	jar, _ := cookiejar.New(nil)
	return &client{t: t, base: base, c: &http.Client{Jar: jar}}, ts
}

func TestEndToEnd(t *testing.T) {
	c, _ := setup(t)

	if _, _, path := c.get("/"); path != "/login" {
		t.Fatalf("anonymous not redirected to login: %s", path)
	}
	if code, _, path := c.get("/auth/start?dev_user=Anna&next=/settings"); code != 200 || path != "/settings" {
		t.Fatalf("login: %d %s", code, path)
	}

	// settings page contains a javascript: bookmarklet (not sanitised away)
	_, body, _ := c.get("/settings")
	if !strings.Contains(body, `href="javascript:`) {
		t.Fatal("bookmarklet missing")
	}

	// share target extracts the URL from text
	_, body, _ = c.get("/share?text=" + url.QueryEscape("bitte drucken https://makerworld.com/de/models/77-x"))
	if !strings.Contains(body, `value="https://makerworld.com/de/models/77-x"`) {
		t.Fatal("share target did not prefill url")
	}

	// add a MakerWorld URL without metadata
	code, body, path := c.post("/add", url.Values{"url": {"https://makerworld.com/de/models/77-x"}, "requested_by": {"Oma"}})
	if code != 200 || !strings.HasPrefix(path, "/items/") || !strings.Contains(body, "Noch keine Bilder") {
		t.Fatalf("add: %d %s", code, path)
	}
	itemPath := path

	// bookmarklet payload enriches the model
	payload := `{"url":"https://makerworld.com/de/models/77-x#profileId-5","og":{"title":"OG"},"design":
		{"id":77,"title":"Halter","instances":[{"id":5,"profileId":990001,"title":"PLA 0.2","prediction":3600}]}}`
	code, body, _ = c.do(http.MethodPost, "/ingest", "application/json", payload)
	if code != 200 || !strings.Contains(body, `"next":"/ingest/`) {
		t.Fatalf("ingest receive: %d %s", code, body)
	}
	next := body[strings.Index(body, "/ingest/"):strings.LastIndex(body, `"`)]
	_, body, _ = c.get(next)
	if !strings.Contains(body, "Halter") || !strings.Contains(body, "Schon erfasst") {
		t.Fatal("ingest confirm page wrong")
	}
	code, body, path = c.post(next, url.Values{"profile_source": {"5"}, "requested_by": {"Ben"}})
	if code != 200 || path == itemPath || !strings.Contains(body, "Halter") || !strings.Contains(body, "profile chosen") {
		t.Fatalf("ingest save: %d %s", code, path)
	}
	// the first item now shows metadata too (same model)
	if _, body, _ = c.get(itemPath); strings.Contains(body, "Noch keine Bilder") || !strings.Contains(body, "PLA 0.2") {
		t.Fatal("model not enriched")
	}

	// status change and overview filter
	c.post(itemPath+"/status", url.Values{"status": {"gedruckt"}})
	if _, body, _ = c.get("/?status=gedruckt"); !strings.Contains(body, "Gedruckt") {
		t.Fatal("overview status")
	}

	// cross-site POST is rejected
	req, _ := http.NewRequest(http.MethodPost, c.base+itemPath+"/delete", nil)
	req.Header.Set("Origin", "https://evil.example")
	resp, _ := c.c.Do(req)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("csrf: %d", resp.StatusCode)
	}

	// manifest has a share target
	if _, body, _ = c.get("/manifest.webmanifest"); !strings.Contains(body, "share_target") {
		t.Fatal("manifest")
	}
}

func TestLoginRedirectIsLocal(t *testing.T) {
	c, _ := setup(t)
	if _, _, path := c.get("/auth/start?dev_user=X&next=//evil.example/"); path != "/" {
		t.Fatalf("open redirect: %s", path)
	}
}
