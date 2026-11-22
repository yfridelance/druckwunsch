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
	// no live MakerWorld calls in tests (SafeClient refuses loopback)
	oldAPI := ingest.MakerWorldAPI
	ingest.MakerWorldAPI = "http://127.0.0.1:1/"
	t.Cleanup(func() { ingest.MakerWorldAPI = oldAPI })
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
	if code, _, path := c.get("/auth/start?dev_user=Anna&dev_role=drucker&next=/settings"); code != 200 || path != "/settings" {
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

func TestRequesterReleaseFlow(t *testing.T) {
	anna, ts := setup(t)
	if _, _, path := anna.get("/auth/start?dev_user=Anna&dev_role=wuenscher"); path != "/" {
		t.Fatalf("login: %s", path)
	}
	jar, _ := cookiejar.New(nil)
	paul := &client{t: t, base: anna.base, c: &http.Client{Jar: jar}}
	_ = ts
	paul.get("/auth/start?dev_user=Paul&dev_role=drucker")

	// requesters see draft/release buttons, no requester picker
	_, body, _ := anna.get("/add")
	if !strings.Contains(body, "Als Entwurf merken") || strings.Contains(body, `name="requester"`) {
		t.Fatal("requester add form")
	}
	_, body, _ = paul.get("/add")
	if !strings.Contains(body, `name="requester"`) || !strings.Contains(body, "Paul (ich)") {
		t.Fatal("printer add form")
	}

	code, body, itemPath := anna.post("/add", url.Values{"url": {"https://makerworld.com/de/models/88-x"}, "release": {"0"}, "requested_by": {"Fremd"}})
	if code != 200 || !strings.Contains(body, "An den Drucker freigeben") || !strings.Contains(body, "Anna") {
		t.Fatalf("draft: %d %s", code, itemPath)
	}
	if code, _, _ := paul.get(itemPath); code != http.StatusNotFound {
		t.Fatalf("printer sees draft: %d", code)
	}
	if _, body, _ = anna.get("/"); !strings.Contains(body, "Alle freigeben") {
		t.Fatal("draft banner missing")
	}
	if _, body, _ = anna.post("/release", url.Values{}); !strings.Contains(body, "1 freigegeben") {
		t.Fatal("release all")
	}
	code, body, _ = paul.get(itemPath)
	if code != 200 || !strings.Contains(body, "Verwerfen") || !strings.Contains(body, `name="requester"`) {
		t.Fatalf("printer view: %d", code)
	}
	if code, _, _ := anna.post(itemPath+"/status", url.Values{"status": {"gedruckt"}}); code != http.StatusForbidden {
		t.Fatalf("requester printed: %d", code)
	}
	if code, _, _ := paul.post(itemPath+"/status", url.Values{"status": {"in_druck"}}); code != 200 {
		t.Fatalf("printer status: %d", code)
	}
	if _, body, _ = anna.get(itemPath); strings.Contains(body, "Zurückziehen") || !strings.Contains(body, "Der Drucker ist dran") {
		t.Fatal("requester view while printing")
	}

	// label management is for printers
	if code, _, _ := anna.post("/labels", url.Values{"name": {"X"}}); code != http.StatusForbidden {
		t.Fatalf("requester created label: %d", code)
	}
	if _, body, _ = anna.get("/settings"); strings.Contains(body, `action="/labels"`) {
		t.Fatal("label form shown to requester")
	}
}

// After the antibot challenge BunkerWeb repeats a form POST as GET; that must
// lead back to the form instead of a 405.
func TestGetOnPostOnlyRoute(t *testing.T) {
	c, _ := setup(t)
	c.get("/auth/start?dev_user=Anna&dev_role=drucker&next=/settings")
	c.c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	for in, want := range map[string]string{
		"/labels": "/settings", "/labels/3/delete": "/settings", "/items/9/labels": "/items/9",
		"/lists/2/share": "/lists/2", "/release": "/", "/auth/logout": "/",
	} {
		resp, err := c.c.Get(c.base + in)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		loc := resp.Header.Get("Location")
		if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(loc, want+"?msg=") {
			t.Errorf("%s: %d %s", in, resp.StatusCode, loc)
		}
	}
	req, _ := http.NewRequest(http.MethodDelete, c.base+"/labels", nil)
	if resp, err := c.c.Do(req); err != nil || resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("DELETE: %v %v", err, resp)
	}
}
