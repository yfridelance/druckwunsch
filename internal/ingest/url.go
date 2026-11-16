// Package ingest turns URLs and bookmarklet payloads into store.Model values.
package ingest

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
)

// Ref identifies a model on a platform.
type Ref struct {
	Platform  string // makerworld, printables, thingiverse, thangs, cults3d, other
	SourceID  string
	ProfileID string // MakerWorld profile ("instance") from #profileId-123, if any
	URL       string // canonical URL used for deduplication
}

var (
	reMakerWorld  = regexp.MustCompile(`^/(?:[a-z]{2}(?:-[a-zA-Z]{2,4})?/)?models/(\d+)`)
	reMWProfile   = regexp.MustCompile(`profileId-(\d+)`)
	rePrintables  = regexp.MustCompile(`^/(?:[a-z]{2}/)?model/(\d+)`)
	reThingiverse = regexp.MustCompile(`^/thing:(\d+)`)
	reURLInText   = regexp.MustCompile(`https?://[^\s<>"']+`)
)

// PlatformNames maps platform keys to display names.
var PlatformNames = map[string]string{
	"makerworld":  "MakerWorld",
	"printables":  "Printables",
	"thingiverse": "Thingiverse",
	"thangs":      "Thangs",
	"cults3d":     "Cults3D",
	"other":       "Andere",
}

// ExtractURL finds the first http(s) URL in free text such as
// "bitte drucken: https://makerworld.com/…".
func ExtractURL(text string) string {
	m := reURLInText.FindString(text)
	return strings.TrimRight(m, ".,;:!?)]}")
}

// Parse normalizes a model URL and detects the platform.
func Parse(raw string) (Ref, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Ref{}, errors.New("keine URL angegeben")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return Ref{}, errors.New("ungültige URL")
	}
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	path := u.EscapedPath()

	switch {
	case host == "makerworld.com" || strings.HasSuffix(host, ".makerworld.com") || host == "makerworld.com.cn":
		if m := reMakerWorld.FindStringSubmatch(path); m != nil {
			ref := Ref{Platform: "makerworld", SourceID: m[1], URL: "https://makerworld.com/models/" + m[1]}
			if p := reMWProfile.FindStringSubmatch(u.Fragment + "&" + u.RawQuery); p != nil {
				ref.ProfileID = p[1]
			} else if p := u.Query().Get("profileId"); p != "" {
				ref.ProfileID = p
			}
			return ref, nil
		}
	case host == "printables.com":
		if m := rePrintables.FindStringSubmatch(path); m != nil {
			return Ref{Platform: "printables", SourceID: m[1], URL: "https://www.printables.com/model/" + m[1]}, nil
		}
	case host == "thingiverse.com":
		if m := reThingiverse.FindStringSubmatch(path); m != nil {
			return Ref{Platform: "thingiverse", SourceID: m[1], URL: "https://www.thingiverse.com/thing:" + m[1]}, nil
		}
	case host == "thangs.com":
		return Ref{Platform: "thangs", URL: cleanURL(u)}, nil
	case host == "cults3d.com":
		return Ref{Platform: "cults3d", URL: cleanURL(u)}, nil
	}
	return Ref{Platform: "other", URL: cleanURL(u)}, nil
}

// cleanURL drops fragments and common tracking parameters.
func cleanURL(u *url.URL) string {
	c := *u
	c.Fragment = ""
	c.Host = strings.ToLower(c.Host)
	q := c.Query()
	for k := range q {
		lk := strings.ToLower(k)
		if strings.HasPrefix(lk, "utm_") || lk == "fbclid" || lk == "gclid" || lk == "from" || lk == "ref" {
			q.Del(k)
		}
	}
	c.RawQuery = q.Encode()
	c.Path = strings.TrimSuffix(c.Path, "/")
	c.RawPath = ""
	return c.String()
}

// MakerWorldProfileURL links directly to one print profile.
func MakerWorldProfileURL(modelID, profileID string) string {
	return "https://makerworld.com/models/" + modelID + "#profileId-" + profileID
}
