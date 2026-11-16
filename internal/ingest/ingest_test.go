package ingest

import (
	"encoding/json"
	"testing"
)

func TestParse(t *testing.T) {
	cases := []struct {
		in, platform, id, profile, url string
	}{
		{"https://makerworld.com/de/models/123456-cable-clip?from=search#profileId-987654", "makerworld", "123456", "987654", "https://makerworld.com/models/123456"},
		{"makerworld.com/en/models/42", "makerworld", "42", "", "https://makerworld.com/models/42"},
		{"https://makerworld.com/models/42-x?profileId=7", "makerworld", "42", "7", "https://makerworld.com/models/42"},
		{"https://www.printables.com/de/model/55555-benchy/files", "printables", "55555", "", "https://www.printables.com/model/55555"},
		{"https://www.thingiverse.com/thing:763622/files", "thingiverse", "763622", "", "https://www.thingiverse.com/thing:763622"},
		{"https://Example.com/a/b/?utm_source=x&keep=1#frag", "other", "", "", "https://example.com/a/b?keep=1"},
	}
	for _, c := range cases {
		r, err := Parse(c.in)
		if err != nil {
			t.Fatalf("%s: %v", c.in, err)
		}
		if r.Platform != c.platform || r.SourceID != c.id || r.ProfileID != c.profile || r.URL != c.url {
			t.Errorf("%s: got %+v", c.in, r)
		}
	}
	for _, bad := range []string{"", "ftp://x/y", "javascript:alert(1)"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("%q: expected error", bad)
		}
	}
}

func TestExtractURL(t *testing.T) {
	got := ExtractURL("Kannst du das bitte drucken? https://makerworld.com/de/models/1-a. Danke!")
	if got != "https://makerworld.com/de/models/1-a" {
		t.Fatal(got)
	}
}

func TestParseOpenGraph(t *testing.T) {
	og := ParseOpenGraph(`<html><head><title>Fallback</title>
		<meta property="og:title" content="Benchy &amp; Co | Printables.com">
		<meta content="https://img.example/x.jpg" property="og:image">
		<meta property="og:site_name" content="Printables.com">
		<meta name="description" content="Ein Boot"></head></html>`)
	if og.Title != "Benchy & Co | Printables.com" || og.Image != "https://img.example/x.jpg" || og.Description != "Ein Boot" {
		t.Fatalf("%+v", og)
	}
	m := ModelFromOpenGraph(Ref{Platform: "printables", URL: "u"}, og)
	if m.Title != "Benchy & Co" || m.MetadataStatus != "ok" {
		t.Fatalf("%+v", m)
	}
}

// Shape modelled on MakerWorld's design data; the real structure is
// verified against a saved page during setup.
const mwDesign = `{"props":{"pageProps":{"design":{
  "id": 123456, "title": "Cable Clip", "titleTranslated": "Kabelclip",
  "coverUrl": "https://makerworld.bblmw.com/makerworld/model/cover.jpg",
  "summary": "<p>Ein <b>praktischer</b> Clip</p>",
  "license": "BY-NC",
  "designCreator": {"name": "Maker Max", "handle": "maxmakes"},
  "designExtension": {"design_pictures": [{"url": "https://makerworld.bblmw.com/a.jpg"}, {"url": "https://makerworld.bblmw.com/makerworld/model/cover.jpg"}]},
  "instances": [
    {"id": 11, "profileId": 987654, "title": "0.2mm PLA", "cover": "https://makerworld.bblmw.com/p1.jpg",
     "prediction": 5400, "weight": 12.34,
     "instanceFilaments": [{"type": "PLA", "color": "#FF0000", "usedG": 10.0}, {"type": "PLA", "color": "#FFFFFF", "usedG": 2.3}],
     "extention": {"modelInfo": {"compatibility": {"devProductName": "X1 Carbon", "nozzleDiameter": 0.4},
        "otherCompatibility": [{"devProductName": "P1S"}], "plates": [{}, {}]}}},
    {"id": 12, "title": "Ohne Profil-ID", "prediction": "60"}
  ]}}}}`

func TestParseMakerWorld(t *testing.T) {
	m, err := ParseMakerWorld([]byte(mwDesign))
	if err != nil {
		t.Fatal(err)
	}
	if m.Title != "Kabelclip" || m.Designer != "Maker Max" || m.DesignerURL != "https://makerworld.com/@maxmakes" {
		t.Errorf("design: %+v", m)
	}
	if m.Description != "Ein praktischer Clip" {
		t.Errorf("description: %q", m.Description)
	}
	if len(m.Images) != 2 {
		t.Errorf("images dedupe: %v", m.Images)
	}
	if len(m.Profiles) != 2 {
		t.Fatalf("profiles: %d", len(m.Profiles))
	}
	p := m.Profiles[0]
	if p.SourceProfileID != "987654" || p.PrintTimeSec != 5400 || p.FilamentG != 12.3 || p.PlateCount != 2 ||
		p.Nozzle != "0.4 mm" || p.Printer != "X1 Carbon (auch: P1S)" || len(p.Materials) != 2 {
		t.Errorf("profile: %+v", p)
	}
	if p.URL != "https://makerworld.com/models/123456#profileId-987654" {
		t.Errorf("profile url: %s", p.URL)
	}
	if m.Profiles[1].SourceProfileID != "12" || m.Profiles[1].PrintTimeSec != 60 {
		t.Errorf("fallback id: %+v", m.Profiles[1])
	}
}

func TestParseMakerWorldDesignOnly(t *testing.T) {
	// The bookmarklet sends just the design object.
	var root map[string]any
	json.Unmarshal([]byte(mwDesign), &root)
	design, _ := json.Marshal(root["props"].(map[string]any)["pageProps"].(map[string]any)["design"])
	m, ref, err := FromPayload(Payload{URL: "https://makerworld.com/de/models/123456-cable-clip#profileId-987654", Design: design})
	if err != nil {
		t.Fatal(err)
	}
	if m.URL != "https://makerworld.com/models/123456" || ref.ProfileID != "987654" || len(m.Profiles) != 2 {
		t.Errorf("%+v %+v", m, ref)
	}
}

func TestFromPayloadOGFallback(t *testing.T) {
	m, _, err := FromPayload(Payload{URL: "https://makerworld.com/models/5", OG: OpenGraph{Title: "Nur OG"}, Design: json.RawMessage("null")})
	if err != nil || m.Title != "Nur OG" {
		t.Fatalf("%v %+v", err, m)
	}
	if _, _, err := FromPayload(Payload{URL: "https://makerworld.com/models/5"}); err == nil {
		t.Fatal("expected error without metadata")
	}
}
