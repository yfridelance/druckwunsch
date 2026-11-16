package ingest

import (
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"math"
	"regexp"
	"strconv"
	"strings"

	"druckwunsch/internal/store"
)

// Payload is what the bookmarklet captures in the user's browser.
type Payload struct {
	URL    string          `json:"url"`
	OG     OpenGraph       `json:"og"`
	Design json.RawMessage `json:"design"` // MakerWorld design object (from __NEXT_DATA__ or the site API)
}

// MaxPayload bounds what we accept from the bookmarklet.
const MaxPayload = 3 << 20

// FromPayload converts a bookmarklet payload into a model.
func FromPayload(p Payload) (store.Model, Ref, error) {
	ref, err := Parse(p.URL)
	if err != nil {
		return store.Model{}, ref, err
	}
	m := ModelFromOpenGraph(ref, p.OG)
	if ref.Platform == "makerworld" && len(p.Design) > 0 && string(p.Design) != "null" {
		mw, err := ParseMakerWorld(p.Design)
		if err == nil {
			mw.Platform, mw.SourceID, mw.URL = ref.Platform, ref.SourceID, ref.URL
			if mw.CoverImage == "" {
				mw.CoverImage = m.CoverImage
			}
			if mw.Description == "" {
				mw.Description = m.Description
			}
			return mw, ref, nil
		}
		// fall back to OpenGraph data
	}
	if m.MetadataStatus != "ok" {
		return m, ref, errors.New("auf der Seite wurden keine Metadaten gefunden")
	}
	return m, ref, nil
}

// ParseMakerWorld reads the design and its print profiles from MakerWorld
// page data: either the design object itself, the site API response or the
// whole __NEXT_DATA__ JSON. Field names are matched tolerantly because
// MakerWorld changes them from time to time.
func ParseMakerWorld(data []byte) (store.Model, error) {
	var root any
	if err := json.Unmarshal(data, &root); err != nil {
		return store.Model{}, fmt.Errorf("Design-JSON: %w", err)
	}
	design := dig(root, "props", "pageProps", "design")
	if _, ok := design.(map[string]any); !ok {
		design = findDesign(root, 0)
	}
	d, ok := design.(map[string]any)
	if !ok {
		return store.Model{}, errors.New("kein MakerWorld-Design in den Seitendaten gefunden")
	}

	m := store.Model{MetadataStatus: "ok"}
	m.SourceID = str(d["id"])
	m.Title = firstStr(d, "titleTranslated", "title", "name")
	m.CoverImage = safeImage(firstStr(d, "coverUrl", "cover", "coverImage"))
	m.Description = stripHTML(firstStr(d, "summaryTranslated", "summary", "description", "details"))
	if lic := d["license"]; lic != nil {
		if lm, ok := lic.(map[string]any); ok {
			m.License = firstStr(lm, "name", "title", "code")
		} else {
			m.License = str(lic)
		}
	}
	if c, ok := firstMap(d, "designCreator", "creator", "user", "designer"); ok {
		m.Designer = firstStr(c, "name", "nickname", "handle")
		if h := firstStr(c, "handle", "username"); h != "" {
			m.DesignerURL = "https://makerworld.com/@" + strings.TrimPrefix(h, "@")
		}
	}

	seen := map[string]bool{}
	addImg := func(u string) {
		if u = safeImage(u); u != "" && !seen[u] {
			seen[u] = true
			m.Images = append(m.Images, u)
		}
	}
	addImg(m.CoverImage)
	if ext, ok := firstMap(d, "designExtension"); ok {
		for _, pic := range arr(ext["design_pictures"]) {
			if pm, ok := pic.(map[string]any); ok {
				addImg(firstStr(pm, "url", "src"))
			}
		}
	}
	for _, pic := range arr(d["pictures"]) {
		if pm, ok := pic.(map[string]any); ok {
			addImg(firstStr(pm, "url", "src"))
		} else {
			addImg(str(pic))
		}
	}
	if len(m.Images) > 30 {
		m.Images = m.Images[:30]
	}

	for _, in := range arr(d["instances"]) {
		im, ok := in.(map[string]any)
		if !ok {
			continue
		}
		p := parseInstance(im)
		if p.SourceProfileID == "" {
			continue
		}
		if m.SourceID != "" {
			p.URL = MakerWorldProfileURL(m.SourceID, p.SourceProfileID)
		}
		m.Profiles = append(m.Profiles, p)
	}
	if m.Title == "" {
		return m, errors.New("Design ohne Titel")
	}
	return m, nil
}

func parseInstance(im map[string]any) store.Profile {
	p := store.Profile{
		SourceProfileID: firstStr(im, "profileId", "id"),
		Name:            firstStr(im, "titleTranslated", "title", "name"),
		CoverImage:      safeImage(firstStr(im, "cover", "coverUrl")),
		PrintTimeSec:    int64(firstNum(im, "prediction", "printTime", "estimatedTime")),
		FilamentG:       math.Round(firstNum(im, "weight", "filamentWeight")*10) / 10,
	}
	modelInfo := dig(im, "extention", "modelInfo")
	if modelInfo == nil {
		modelInfo = dig(im, "extension", "modelInfo")
	}
	if mi, ok := modelInfo.(map[string]any); ok {
		if comp, ok := firstMap(mi, "compatibility"); ok {
			p.Printer = firstStr(comp, "devProductName", "devModelName", "printerName")
			if nd := firstNum(comp, "nozzleDiameter"); nd > 0 {
				p.Nozzle = strconv.FormatFloat(nd, 'f', -1, 64) + " mm"
			}
		}
		var others []string
		for _, oc := range arr(mi["otherCompatibility"]) {
			if om, ok := oc.(map[string]any); ok {
				if n := firstStr(om, "devProductName", "devModelName"); n != "" {
					others = append(others, n)
				}
			}
		}
		if len(others) > 0 {
			if p.Printer != "" {
				p.Printer += " (auch: " + strings.Join(others, ", ") + ")"
			} else {
				p.Printer = strings.Join(others, ", ")
			}
		}
		p.PlateCount = len(arr(mi["plates"]))
	}
	if p.PlateCount == 0 {
		p.PlateCount = int(firstNum(im, "plateCount", "plateCnt"))
	}
	for _, f := range arr(im["instanceFilaments"]) {
		if fm, ok := f.(map[string]any); ok {
			p.Materials = append(p.Materials, store.Material{
				Type:  firstStr(fm, "type", "filamentType", "material"),
				Color: firstStr(fm, "color", "colour"),
				Grams: math.Round(firstNum(fm, "usedG", "weight")*10) / 10,
			})
		}
	}
	if p.FilamentG == 0 {
		for _, mat := range p.Materials {
			p.FilamentG += mat.Grams
		}
	}
	return p
}

// findDesign searches depth-first for an object that looks like a design:
// it has a title and an "instances" array.
func findDesign(v any, depth int) any {
	if depth > 12 {
		return nil
	}
	switch t := v.(type) {
	case map[string]any:
		if _, ok := t["instances"].([]any); ok && firstStr(t, "title", "titleTranslated") != "" {
			return t
		}
		for _, c := range t {
			if r := findDesign(c, depth+1); r != nil {
				return r
			}
		}
	case []any:
		for _, c := range t {
			if r := findDesign(c, depth+1); r != nil {
				return r
			}
		}
	}
	return nil
}

func dig(v any, keys ...string) any {
	for _, k := range keys {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[k]
	}
	return v
}

func arr(v any) []any {
	a, _ := v.([]any)
	return a
}

func str(v any) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case json.Number:
		return t.String()
	}
	return ""
}

func firstStr(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s := str(m[k]); s != "" {
			return s
		}
	}
	return ""
}

func firstNum(m map[string]any, keys ...string) float64 {
	for _, k := range keys {
		switch t := m[k].(type) {
		case float64:
			return t
		case string:
			if f, err := strconv.ParseFloat(t, 64); err == nil {
				return f
			}
		}
	}
	return 0
}

func firstMap(m map[string]any, keys ...string) (map[string]any, bool) {
	for _, k := range keys {
		if v, ok := m[k].(map[string]any); ok {
			return v, true
		}
	}
	return nil, false
}

var (
	reTags   = regexp.MustCompile(`(?s)<[^>]*>`)
	reBlanks = regexp.MustCompile(`\n{3,}`)
)

func stripHTML(s string) string {
	s = strings.NewReplacer("<br>", "\n", "<br/>", "\n", "<br />", "\n", "</p>", "\n\n", "</li>", "\n").Replace(s)
	s = html.UnescapeString(reTags.ReplaceAllString(s, ""))
	s = reBlanks.ReplaceAllString(strings.TrimSpace(s), "\n\n")
	if len(s) > 5000 {
		s = s[:5000] + "…"
	}
	return s
}
