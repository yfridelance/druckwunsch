// Druckwunsch collects 3D model links (MakerWorld & co.) as print requests.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"druckwunsch/internal/auth"
	"druckwunsch/internal/ingest"
	"druckwunsch/internal/store"
	"druckwunsch/internal/web"
)

func env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

// version is set at build time (-ldflags "-X main.version=...").
var version = "dev"

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, nil)))
	if err := run(); err != nil {
		slog.Error("Abbruch", "err", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	dataDir := env("DW_DATA_DIR", "./data")
	baseURL := strings.TrimSuffix(env("DW_BASE_URL", "http://localhost:8080"), "/")
	listen := env("DW_LISTEN", ":8080")
	if err := os.MkdirAll(filepath.Join(dataDir, "images"), 0o755); err != nil {
		return err
	}

	st, err := store.Open(filepath.Join(dataDir, "druckwunsch.db"))
	if err != nil {
		return err
	}
	defer st.Close()

	a, err := auth.New(ctx, auth.Config{
		BaseURL:      baseURL,
		Issuer:       env("DW_OIDC_ISSUER", ""),
		ClientID:     env("DW_OIDC_CLIENT_ID", ""),
		ClientSecret: env("DW_OIDC_CLIENT_SECRET", ""),
		DevLogin:     env("DW_DEV_LOGIN", "") == "true",
		GroupsClaim:  env("DW_OIDC_GROUPS_CLAIM", "groups"),
		PrinterGroup: env("DW_GROUP_PRINTER", "druckwunsch_full"),
		UserGroup:    env("DW_GROUP_USER", "druckwunsch_limited"),
	}, st)
	if err != nil {
		return err
	}

	fetcher := ingest.SafeClient(20 * time.Second)
	srv := &web.Server{
		BaseURL:  baseURL,
		Store:    st,
		Auth:     a,
		Images:   &ingest.ImageCache{Dir: filepath.Join(dataDir, "images"), Client: fetcher},
		Fetcher:  fetcher,
		OIDCName: env("DW_OIDC_NAME", "SSO"),
	}
	h, err := srv.Handler()
	if err != nil {
		return err
	}

	go func() {
		t := time.NewTicker(15 * time.Minute)
		defer t.Stop()
		for {
			if err := st.Cleanup(ctx); err != nil && ctx.Err() == nil {
				slog.Warn("cleanup", "err", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()

	hs := &http.Server{Addr: listen, Handler: h, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		hs.Shutdown(sctx)
	}()
	slog.Info("Druckwunsch läuft", "version", version, "listen", listen, "base_url", baseURL)
	if err := hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
