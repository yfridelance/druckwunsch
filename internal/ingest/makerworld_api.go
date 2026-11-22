package ingest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"druckwunsch/internal/store"
)

// MakerWorldAPI is the (undocumented) design endpoint the MakerWorld website
// itself uses. Unlike the model pages it is not behind the Cloudflare
// challenge (checked 22.11.2026). It may change or get blocked at any time;
// the bookmarklet stays as fallback.
var MakerWorldAPI = "https://makerworld.com/api/v1/design-service/design/"

// ErrNotFound means the platform does not know the model.
var ErrNotFound = errors.New("Modell nicht gefunden")

// FetchMakerWorld loads a design with all print profiles by model id.
func FetchMakerWorld(ctx context.Context, c *http.Client, ref Ref) (store.Model, error) {
	if ref.Platform != "makerworld" || ref.SourceID == "" || strings.Trim(ref.SourceID, "0123456789") != "" {
		return store.Model{}, errors.New("keine MakerWorld-Modell-ID")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, MakerWorldAPI+url.PathEscape(ref.SourceID), nil)
	if err != nil {
		return store.Model{}, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return store.Model{}, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return store.Model{}, ErrNotFound
	case resp.StatusCode != http.StatusOK:
		if resp.Header.Get("Cf-Mitigated") != "" {
			return store.Model{}, fmt.Errorf("HTTP %d (Bot-Schutz)", resp.StatusCode)
		}
		return store.Model{}, fmt.Errorf("HTTP %d", resp.StatusCode)
	case !strings.Contains(resp.Header.Get("Content-Type"), "json"):
		return store.Model{}, errors.New("Antwort ist kein JSON (Bot-Schutz?)")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxPayload+1))
	if err != nil {
		return store.Model{}, err
	}
	if len(body) > MaxPayload {
		return store.Model{}, errors.New("Antwort zu gross")
	}
	m, err := ParseMakerWorld(body)
	if err != nil {
		return store.Model{}, err
	}
	m.Platform, m.SourceID, m.URL = ref.Platform, ref.SourceID, ref.URL
	return m, nil
}

// FetchModel fetches metadata server-side: MakerWorld through its API, other
// platforms through OpenGraph.
func FetchModel(ctx context.Context, c *http.Client, ref Ref) (store.Model, error) {
	if ref.Platform == "makerworld" {
		return FetchMakerWorld(ctx, c, ref)
	}
	og, err := FetchOpenGraph(ctx, c, ref.URL)
	if err != nil {
		return store.Model{}, err
	}
	return ModelFromOpenGraph(ref, og), nil
}
