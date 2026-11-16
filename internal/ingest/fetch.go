package ingest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"io"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"druckwunsch/internal/store"
)

const userAgent = "Mozilla/5.0 (compatible; Druckwunsch/1.0)"

// SafeClient refuses to connect to loopback, private and link-local
// addresses, so user-supplied URLs cannot reach internal services (SSRF).
func SafeClient(timeout time.Duration) *http.Client {
	dialer := &net.Dialer{
		Timeout: 10 * time.Second,
		Control: func(network, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			ip := net.ParseIP(host)
			if ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
				ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() || isCGNAT(ip) {
				return fmt.Errorf("Ziel %s ist nicht erlaubt", host)
			}
			return nil
		},
	}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext:         dialer.DialContext,
			TLSHandshakeTimeout: 10 * time.Second,
			MaxIdleConns:        10,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("zu viele Weiterleitungen")
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return errors.New("ungültiges Weiterleitungsziel")
			}
			return nil
		},
	}
}

var cgnat = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

func isCGNAT(ip net.IP) bool { return cgnat.Contains(ip) }

var (
	reMeta  = regexp.MustCompile(`(?is)<meta\s+[^>]*>`)
	reAttr  = regexp.MustCompile(`(?is)([a-z:_-]+)\s*=\s*("([^"]*)"|'([^']*)')`)
	reTitle = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
)

// OpenGraph holds the generic metadata a page announces.
type OpenGraph struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Image       string `json:"image"`
	SiteName    string `json:"siteName"`
	Author      string `json:"author"`
}

// ParseOpenGraph extracts og:/twitter: metadata from HTML.
func ParseOpenGraph(page string) OpenGraph {
	var og OpenGraph
	for _, tag := range reMeta.FindAllString(page, -1) {
		attrs := map[string]string{}
		for _, a := range reAttr.FindAllStringSubmatch(tag, -1) {
			v := a[3]
			if v == "" {
				v = a[4]
			}
			attrs[strings.ToLower(a[1])] = html.UnescapeString(v)
		}
		key := attrs["property"]
		if key == "" {
			key = attrs["name"]
		}
		val := strings.TrimSpace(attrs["content"])
		if val == "" {
			continue
		}
		switch strings.ToLower(key) {
		case "og:title", "twitter:title":
			if og.Title == "" {
				og.Title = val
			}
		case "og:description", "twitter:description", "description":
			if og.Description == "" {
				og.Description = val
			}
		case "og:image", "og:image:url", "twitter:image":
			if og.Image == "" {
				og.Image = val
			}
		case "og:site_name":
			og.SiteName = val
		case "author", "article:author":
			og.Author = val
		}
	}
	if og.Title == "" {
		if m := reTitle.FindStringSubmatch(page); m != nil {
			og.Title = strings.TrimSpace(html.UnescapeString(m[1]))
		}
	}
	return og
}

// FetchOpenGraph downloads a page and reads its metadata. Pages behind a bot
// check (Cloudflare "Just a moment…") are reported as an error.
func FetchOpenGraph(ctx context.Context, c *http.Client, pageURL string) (OpenGraph, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return OpenGraph{}, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html")
	resp, err := c.Do(req)
	if err != nil {
		return OpenGraph{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return OpenGraph{}, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return OpenGraph{}, err
	}
	og := ParseOpenGraph(string(body))
	if og.Title == "" || strings.Contains(og.Title, "Just a moment") {
		return OpenGraph{}, errors.New("Seite liefert keine Metadaten (Bot-Schutz?)")
	}
	return og, nil
}

// ModelFromOpenGraph builds a model from generic page metadata.
func ModelFromOpenGraph(ref Ref, og OpenGraph) store.Model {
	m := store.Model{
		Platform: ref.Platform, SourceID: ref.SourceID, URL: ref.URL,
		Title: cleanTitle(og.Title, og.SiteName), Description: og.Description,
		Designer: og.Author, CoverImage: safeImage(og.Image),
	}
	if m.CoverImage != "" {
		m.Images = []string{m.CoverImage}
	}
	if m.Title != "" {
		m.MetadataStatus = "ok"
	}
	return m
}

func cleanTitle(title, site string) string {
	title = strings.TrimSpace(title)
	for _, sep := range []string{" | ", " - ", " – ", " by "} {
		if site != "" && strings.HasSuffix(title, sep+site) {
			title = strings.TrimSuffix(title, sep+site)
		}
	}
	return title
}

// safeImage only accepts absolute http(s) image URLs.
func safeImage(u string) string {
	u = strings.TrimSpace(u)
	if strings.HasPrefix(u, "//") {
		u = "https:" + u
	}
	if strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "http://") {
		if len(u) < 2048 {
			return u
		}
	}
	return ""
}

// ImageCache stores remote images on disk, keyed by URL hash.
type ImageCache struct {
	Dir    string
	Client *http.Client
}

// LocalPrefix is the URL path under which cached images are served.
const LocalPrefix = "/img/"

// Cache downloads src and returns the local URL; on failure it returns src.
func (ic *ImageCache) Cache(ctx context.Context, src string) string {
	if src == "" || strings.HasPrefix(src, LocalPrefix) {
		return src
	}
	sum := sha256.Sum256([]byte(src))
	name := hex.EncodeToString(sum[:16])
	if matches, _ := filepath.Glob(filepath.Join(ic.Dir, name+".*")); len(matches) > 0 {
		return LocalPrefix + filepath.Base(matches[0])
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
	if err != nil {
		return src
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := ic.Client.Do(req)
	if err != nil {
		return src
	}
	defer resp.Body.Close()
	ct, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	ext := map[string]string{"image/jpeg": ".jpg", "image/png": ".png", "image/webp": ".webp", "image/gif": ".gif", "image/avif": ".avif"}[ct]
	if resp.StatusCode != http.StatusOK || ext == "" {
		return src
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20+1))
	if err != nil || len(data) > 10<<20 {
		return src
	}
	if err := os.MkdirAll(ic.Dir, 0o755); err != nil {
		return src
	}
	tmp := filepath.Join(ic.Dir, name+ext+".tmp")
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return src
	}
	if err := os.Rename(tmp, filepath.Join(ic.Dir, name+ext)); err != nil {
		return src
	}
	return LocalPrefix + name + ext
}
