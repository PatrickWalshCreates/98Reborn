package tool

import (
	"testing"
	"time"
)

func TestNewestBaseByDate(t *testing.T) {
	asset := func(names ...string) (as []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	}) {
		for _, n := range names {
			as = append(as, struct {
				Name string `json:"name"`
				URL  string `json:"browser_download_url"`
			}{n, "https://example.invalid/" + n})
		}
		return
	}
	day := func(d int) time.Time { return time.Date(2026, 11, d, 12, 0, 0, 0, time.UTC) }
	base := asset("98reborn-base.iso", "98reborn-base.iso.sha256")
	// The order GitHub returns: by tag name as text.
	rs := []release{
		{Tag: "v0.1.0-alpha.9", Published: day(27), Assets: base},
		{Tag: "v0.1.0-alpha.12", Published: day(30), Draft: true, Assets: base},
		{Tag: "v0.1.0-alpha.11", Published: day(28), Assets: base},
		{Tag: "v0.1.0-alpha.10", Published: day(28).Add(-time.Hour), Assets: base},
		{Tag: "v0.1.0-alpha.13", Published: day(29), Assets: asset("notes.txt")},
	}
	tag, _, _, err := newestBase(rs)
	if err != nil || tag != "v0.1.0-alpha.11" {
		t.Fatalf("got %q, %v; want v0.1.0-alpha.11 (newest published with a base image, not a draft)", tag, err)
	}
}
