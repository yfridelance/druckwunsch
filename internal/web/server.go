// Package web contains the HTTP handlers and templates.
package web

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"druckwunsch/internal/auth"
	"druckwunsch/internal/ingest"
	"druckwunsch/internal/store"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

type Server struct {
	BaseURL  string
	Store    *store.Store
	Auth     *auth.Auth
	Images   *ingest.ImageCache
	Fetcher  *http.Client
	OIDCName string

	pages map[string]*template.Template
	base  *url.URL
}

func (s *Server) Handler() (http.Handler, error) {
	var err error
	if s.base, err = url.Parse(s.BaseURL); err != nil {
		return nil, err
	}
	if err := s.loadTemplates(); err != nil {
		return nil, err
	}

	r := chi.NewRouter()
	r.Use(middleware.RealIP, middleware.Recoverer, securityHeaders, s.Auth.Middleware)

	static, _ := fs.Sub(staticFS, "static")
	r.Handle("/static/*", http.StripPrefix("/static/", cacheFor(http.FileServer(http.FS(static)), 24*time.Hour)))
	r.Handle("/img/*", http.StripPrefix("/img/", cacheFor(http.FileServer(noDirFS{http.Dir(s.Images.Dir)}), 30*24*time.Hour)))
	r.Get("/manifest.webmanifest", s.manifest)
	r.Get("/sw.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript")
		w.Header().Set("Service-Worker-Allowed", "/")
		b, _ := staticFS.ReadFile("static/sw.js")
		w.Write(b)
	})
	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })

	r.Get("/login", s.loginPage)
	r.Get("/auth/start", s.Auth.Start)
	r.Get("/auth/callback", s.Auth.Callback)
	r.Post("/auth/logout", s.Auth.Logout)

	// Bookmarklet landing page: public, because the payload sits in the URL
	// fragment, which would be lost during the OIDC redirects.
	r.Get("/ingest", s.ingestLanding)
	r.Post("/ingest", s.ingestReceive)

	r.Group(func(r chi.Router) {
		r.Use(s.Auth.Require, s.sameOrigin)
		r.Get("/", s.overview)
		r.Get("/add", s.addForm)
		r.Post("/add", s.addSubmit)
		r.Get("/share", s.shareTarget)
		r.Get("/ingest/{token}", s.ingestConfirm)
		r.Post("/ingest/{token}", s.ingestSave)

		r.Get("/items/{id}", s.itemPage)
		r.Post("/items/{id}/status", s.itemStatus)
		r.Post("/items/{id}/edit", s.itemEdit)
		r.Post("/items/{id}/title", s.itemTitle)
		r.Post("/items/{id}/profile", s.itemProfile)
		r.Post("/items/{id}/labels", s.itemLabels)
		r.Post("/items/{id}/comment", s.itemComment)
		r.Post("/items/{id}/move", s.itemMove)
		r.Post("/items/{id}/delete", s.itemDelete)
		r.Post("/items/{id}/refresh", s.itemRefresh)

		r.Get("/lists", s.listsPage)
		r.Post("/lists", s.listCreate)
		r.Get("/lists/{id}", s.listPage)
		r.Post("/lists/{id}/rename", s.listRename)
		r.Post("/lists/{id}/share", s.listShare)
		r.Post("/lists/{id}/unshare", s.listUnshare)
		r.Post("/lists/{id}/delete", s.listDelete)

		r.Get("/settings", s.settingsPage)
		r.Post("/labels", s.labelCreate)
		r.Post("/labels/{id}/delete", s.labelDelete)
	})
	return r, nil
}

// ---- middleware ----

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' https: data:; style-src 'self' 'unsafe-inline'; script-src 'self'; frame-ancestors 'none'; form-action 'self'")
		next.ServeHTTP(w, r)
	})
}

// sameOrigin rejects cross-site state-changing requests (CSRF), in addition
// to the SameSite=Lax session cookie.
func (s *Server) sameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			src := r.Header.Get("Origin")
			if src == "" || src == "null" {
				src = r.Header.Get("Referer")
			}
			u, err := url.Parse(src)
			if src == "" || err != nil || !strings.EqualFold(u.Host, s.base.Host) {
				http.Error(w, "Anfrage von fremder Herkunft abgelehnt", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func cacheFor(h http.Handler, d time.Duration) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", int(d.Seconds())))
		h.ServeHTTP(w, r)
	})
}

// noDirFS hides directory listings.
type noDirFS struct{ fs http.FileSystem }

func (n noDirFS) Open(name string) (http.File, error) {
	f, err := n.fs.Open(name)
	if err != nil {
		return nil, err
	}
	if st, err := f.Stat(); err == nil && st.IsDir() {
		f.Close()
		return nil, fs.ErrNotExist
	}
	return f, nil
}

// ---- templates ----

var funcs = template.FuncMap{
	"statusLabel":  store.StatusLabel,
	"platformName": func(p string) string { return ingest.PlatformNames[p] },
	"statuses":     func() []store.Status { return store.Statuses },
	"duration": func(sec int64) string {
		if sec <= 0 {
			return "–"
		}
		h, m := sec/3600, (sec%3600)/60
		if h == 0 {
			return fmt.Sprintf("%d min", m)
		}
		return fmt.Sprintf("%d h %02d min", h, m)
	},
	"date": func(t any) string {
		switch v := t.(type) {
		case time.Time:
			return v.Local().Format("02.01.2006 15:04")
		case *time.Time:
			if v != nil {
				return v.Local().Format("02.01.2006 15:04")
			}
		}
		return ""
	},
	"grams": func(g float64) string {
		if g <= 0 {
			return "–"
		}
		return strconv.FormatFloat(g, 'f', 1, 64) + " g"
	},
	"isChosen": func(id int64, chosen *int64) bool { return chosen != nil && *chosen == id },
	"hasLabel": func(id int64, ls []store.Label) bool {
		for _, l := range ls {
			if l.ID == id {
				return true
			}
		}
		return false
	},
	"safeColor": func(c string) template.CSS {
		if len(c) <= 9 && strings.HasPrefix(c, "#") && strings.Trim(c[1:], "0123456789abcdefABCDEF") == "" {
			return template.CSS(c)
		}
		return template.CSS("#888888")
	},
	"jsURL": func(s string) template.URL { return template.URL(s) }, // only for the generated bookmarklet
	"layer": layerClass,
	"step":  statusStep,
	"seq": func(n int) []int {
		r := make([]int, n)
		for i := range r {
			r[i] = i
		}
		return r
	},
	"initial": func(s string) string {
		for _, r := range s {
			return strings.ToUpper(string(r))
		}
		return "?"
	},
	"workflow": func() []store.Status { return store.Statuses[:4] },
}

// statusStep maps a status to its position in the workflow (-1 = discarded).
func statusStep(status string) int {
	for i, s := range store.Statuses[:4] {
		if s.Key == status {
			return i
		}
	}
	return -1
}

// layerClass styles segment i of the layer progress bar.
func layerClass(i int, status string) string {
	st := statusStep(status)
	switch {
	case st < 0 || i > st:
		return ""
	case i == st && status == "in_druck":
		return "now"
	default:
		return "on"
	}
}

func (s *Server) loadTemplates() error {
	layout, err := templateFS.ReadFile("templates/layout.html")
	if err != nil {
		return err
	}
	names, _ := fs.Glob(templateFS, "templates/*.html")
	s.pages = map[string]*template.Template{}
	for _, n := range names {
		base := filepath.Base(n)
		if base == "layout.html" {
			continue
		}
		body, _ := templateFS.ReadFile(n)
		t, err := template.New("layout").Funcs(funcs).Parse(string(layout))
		if err != nil {
			return err
		}
		if _, err := t.Parse(string(body)); err != nil {
			return fmt.Errorf("%s: %w", n, err)
		}
		s.pages[strings.TrimSuffix(base, ".html")] = t
	}
	return nil
}

type pageData struct {
	Title    string
	User     store.User
	LoggedIn bool
	Nav      string
	Flash    string
	Data     any
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, page, title string, data any) {
	t, ok := s.pages[page]
	if !ok {
		http.Error(w, "Template fehlt: "+page, http.StatusInternalServerError)
		return
	}
	u, logged := auth.User(r.Context())
	pd := pageData{Title: title, User: u, LoggedIn: logged, Nav: page, Flash: r.URL.Query().Get("msg"), Data: data}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(w, "layout", pd); err != nil {
		slog.Error("render", "page", page, "err", err)
	}
}

func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		http.Error(w, "Nicht gefunden", http.StatusNotFound)
	case errors.Is(err, store.ErrForbidden):
		http.Error(w, "Keine Berechtigung", http.StatusForbidden)
	default:
		slog.Error("request failed", "path", r.URL.Path, "err", err)
		http.Error(w, "Interner Fehler", http.StatusInternalServerError)
	}
}

func redirect(w http.ResponseWriter, r *http.Request, to, msg string) {
	if msg != "" {
		sep := "?"
		if strings.Contains(to, "?") {
			sep = "&"
		}
		to += sep + "msg=" + url.QueryEscape(msg)
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}

func me(r *http.Request) store.User {
	u, _ := auth.User(r.Context())
	return u
}

func urlParam(r *http.Request, name string) string { return chi.URLParam(r, name) }

func idParam(r *http.Request, name string) int64 {
	id, _ := strconv.ParseInt(chi.URLParam(r, name), 10, 64)
	return id
}

func formInt(r *http.Request, name string) int64 {
	v, _ := strconv.ParseInt(strings.TrimSpace(r.FormValue(name)), 10, 64)
	return v
}

func formStr(r *http.Request, name string, max int) string {
	v := strings.TrimSpace(r.FormValue(name))
	if len([]rune(v)) > max {
		v = string([]rune(v)[:max])
	}
	return v
}

// cacheImages stores a model's images locally in the background.
func (s *Server) cacheImages(modelID int64) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		m, err := s.Store.Model(ctx, modelID)
		if err != nil {
			return
		}
		cover := s.Images.Cache(ctx, m.CoverImage)
		imgs := make([]string, 0, len(m.Images))
		for _, i := range m.Images {
			imgs = append(imgs, s.Images.Cache(ctx, i))
		}
		if err := s.Store.SetImageURLs(ctx, modelID, cover, imgs); err != nil {
			slog.Warn("image cache", "err", err)
		}
		for _, p := range m.Profiles {
			if c := s.Images.Cache(ctx, p.CoverImage); c != p.CoverImage {
				_ = s.Store.SetProfileCover(ctx, p.ID, c)
			}
		}
	}()
}
