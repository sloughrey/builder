package presets

import (
	"strings"
	"testing"
	"time"
)

// The embedded presets must be consistent: unique ids, every book's series is
// a universe, and every universe has a title.
func TestEmbeddedPresetsAreConsistent(t *testing.T) {
	p, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, b := range p.Books {
		id := normalizeId(b.Id)
		if seen[id] {
			t.Errorf("duplicate book id %q", b.Id)
		}
		seen[id] = true
		if _, ok := p.Universes[b.Series]; !ok {
			t.Errorf("book %q has series %q, which is not a universe", b.Id, b.Series)
		}
	}
	for id, u := range p.Universes {
		if id != normalizeId(id) {
			t.Errorf("universe id %q must be lower case", id)
		}
		if u.Title == "" {
			t.Errorf("universe %q has no title", id)
		}
	}
}

func TestBeforeHistory(t *testing.T) {
	p, err := Parse([]byte(`{"historyStart": "2010-06-13", "bufferDays": 3, "books": [
		{"id": "old", "title": "Old", "series": "s", "released": "2009-06-09"},
		{"id": "new", "title": "New", "series": "s", "released": "2010-08-31"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]bool{"old": true, "new": false} {
		b, _ := p.Book(id)
		cutoff, _ := p.Cutoff(b, -1)
		if got := p.BeforeHistory(cutoff); got != want {
			t.Errorf("BeforeHistory(%s) = %v, want %v", id, got, want)
		}
	}
	if (Presets{}).BeforeHistory(time.Time{}) {
		t.Error("no historyStart should never be before history")
	}

	var out strings.Builder
	p.Print(&out)
	if !strings.Contains(out.String(), "* old") || strings.Contains(out.String(), "* new") {
		t.Errorf("Print should mark only the book before history:\n%s", out.String())
	}

	if _, err := Parse([]byte(`{"historyStart": "June 2010"}`)); err == nil {
		t.Error("invalid historyStart should fail to parse")
	}
}
