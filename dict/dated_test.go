package dict

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/runik-3/builder/internal/fakewiki"
	"github.com/runik-3/builder/presets"
	wikibot "github.com/runik-3/builder/wikiBot"
)

// A small Roshar: the cutoff is just before Rhythm of War (2020-11-14).
func roshar() *fakewiki.Wiki {
	return &fakewiki.Wiki{
		Pages: []fakewiki.Page{
			{Title: "Kaladin", Categories: []string{"Stormlight Archive"}, Revs: []fakewiki.Rev{
				fakewiki.R(1, "2010-09-01", "'''Kaladin''' is a soldier from Hearthstone."),
				fakewiki.R(2, "2021-01-01", "'''Kaladin''' is a FUTURE SPOILER."),
			}},
			{Title: "Kal", Revs: []fakewiki.Rev{fakewiki.R(3, "2012-01-01", "#REDIRECT [[Kaladin]]")}},
			{Title: "Stormblessed", Revs: []fakewiki.Rev{fakewiki.R(4, "2021-02-01", "#REDIRECT [[Kaladin]]")}},
			{Title: "Lift", Categories: []string{"Stormlight Archive"}, Revs: []fakewiki.Rev{
				fakewiki.R(5, "2016-12-01", "'''Lift''' is a young Reshi girl."),
			}},
			{Title: "New Character", Categories: []string{"Stormlight Archive"}, Revs: []fakewiki.Rev{
				fakewiki.R(6, "2021-03-01", "'''New Character''' only appears in book four."),
			}},
			// Renamed after the cutoff: the new name is itself a spoiler.
			{Title: "Spoiler Name", Categories: []string{"Stormlight Archive"}, Revs: []fakewiki.Rev{
				fakewiki.R(7, "2011-01-01", "'''Masked Stranger''' is a mysterious figure."),
			}},
			{Title: "Masked Stranger", Revs: []fakewiki.Rev{fakewiki.R(8, "2022-01-01", "#REDIRECT [[Spoiler Name]]")}},
			{Title: "Dalinar", Categories: []string{"Radiants"}, Revs: []fakewiki.Rev{
				fakewiki.R(9, "2010-09-01", "'''Dalinar''' is a highprince of Alethkar."),
			}},
			{Title: "Deep Lore", Categories: []string{"Deep"}, Revs: []fakewiki.Rev{
				fakewiki.R(10, "2015-01-01", "'''Deep Lore''' is buried two levels down."),
			}},
			{Title: "Vin", Categories: []string{"Mistborn"}, Revs: []fakewiki.Rev{
				fakewiki.R(11, "2006-08-01", "'''Vin''' is a street urchin."),
			}},
		},
		Subcats: map[string][]string{
			"Stormlight Archive": {"Radiants"},
			"Radiants":           {"Deep"},
		},
		Moves: []fakewiki.Move{{From: "Masked Stranger", To: "Spoiler Name", At: "2022-01-01"}},
	}
}

var rowCutoff = time.Date(2020, 11, 14, 0, 0, 0, 0, time.UTC)

func build(t *testing.T, w *fakewiki.Wiki, opts DatedOptions) (Dict, DatedStats) {
	t.Helper()
	Quiet = true
	srv := w.Server()
	t.Cleanup(srv.Close)
	opts.Client.Rate = 1000
	opts.Client.BackoffBase = time.Millisecond
	d := Dict{}
	wiki := wikibot.WikiDetails{ApiUrl: srv.URL + "/api.php"}
	stats, err := d.GenerateDatedDefinitions(wiki, GeneratorOptions{Depth: 1}, opts)
	if err != nil {
		t.Fatal(err)
	}
	return d, stats
}

func words(d Dict) []string {
	out := []string{}
	for _, e := range d.Lexicon {
		out = append(out, e.Word)
	}
	sort.Strings(out)
	return out
}

func TestDatedStormlightBuild(t *testing.T) {
	d, stats := build(t, roshar(), DatedOptions{
		Cutoff:        rowCutoff,
		Categories:    []string{"Stormlight Archive"},
		CategoryDepth: 1,
	})

	got := strings.Join(words(d), ",")
	if got != "Dalinar,Kaladin,Lift,Masked Stranger" {
		t.Fatalf("unexpected entries: %s", got)
	}

	k, _ := d.Lexicon.Find("Kaladin")
	if strings.Contains(k.Definition, "SPOILER") || !strings.Contains(k.Definition, "soldier") {
		t.Fatalf("Kaladin should use the pre-cutoff text, got %q", k.Definition)
	}
	if strings.Join(k.Synonyms, ",") != "Kal" {
		t.Fatalf("only redirects that existed at the cutoff should be synonyms, got %v", k.Synonyms)
	}
	if stats.NotYetCreated != 1 || stats.Renamed != 1 || stats.LateRedirects < 1 {
		t.Fatalf("unexpected stats %+v", stats)
	}
}

func TestDatedWholeWikiWithLaterCutoff(t *testing.T) {
	d, _ := build(t, roshar(), DatedOptions{Cutoff: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)})
	got := strings.Join(words(d), ",")
	if got != "Dalinar,Deep Lore,Kaladin,Lift,New Character,Spoiler Name,Vin" {
		t.Fatalf("unexpected entries: %s", got)
	}
	k, _ := d.Lexicon.Find("Kaladin")
	if strings.Join(k.Synonyms, ",") != "Kal,Stormblessed" {
		t.Fatalf("got synonyms %v", k.Synonyms)
	}
}

func TestDatedExcludeAndNoSynonyms(t *testing.T) {
	d, _ := build(t, roshar(), DatedOptions{
		Cutoff:        rowCutoff,
		Categories:    []string{"Stormlight Archive"},
		CategoryDepth: 2,
		Exclude:       []string{"Lift"},
		SkipRedirects: true,
	})
	got := strings.Join(words(d), ",")
	if got != "Dalinar,Deep Lore,Kaladin,Masked Stranger" {
		t.Fatalf("unexpected entries: %s", got)
	}
	k, _ := d.Lexicon.Find("Kaladin")
	if len(k.Synonyms) != 0 {
		t.Fatalf("expected no synonyms, got %v", k.Synonyms)
	}
}

func TestDatedRetriesAfterThrottling(t *testing.T) {
	w := roshar()
	w.FailFirst = 2
	d, _ := build(t, w, DatedOptions{Cutoff: rowCutoff, Categories: []string{"Radiants"}})
	if strings.Join(words(d), ",") != "Dalinar" {
		t.Fatalf("unexpected entries: %v", words(d))
	}
}

func TestDatedCacheAvoidsRepeatRequests(t *testing.T) {
	w := roshar()
	cache := t.TempDir()
	opts := DatedOptions{Cutoff: rowCutoff, Categories: []string{"Stormlight Archive"}, Client: wikibot.ClientOptions{CacheDir: cache}}
	build(t, w, opts)
	first := w.Requests.Load()
	if first == 0 {
		t.Fatal("expected requests")
	}
	// Same server address is needed for cache keys, so reuse one server.
	srv := w.Server()
	defer srv.Close()
	Quiet = true
	opts.Client.Rate = 1000
	d1 := Dict{}
	if _, err := d1.GenerateDatedDefinitions(wikibot.WikiDetails{ApiUrl: srv.URL + "/api.php"}, GeneratorOptions{Depth: 1}, opts); err != nil {
		t.Fatal(err)
	}
	afterFirst := w.Requests.Load()
	d2 := Dict{}
	if _, err := d2.GenerateDatedDefinitions(wikibot.WikiDetails{ApiUrl: srv.URL + "/api.php"}, GeneratorOptions{Depth: 1}, opts); err != nil {
		t.Fatal(err)
	}
	if w.Requests.Load() != afterFirst {
		t.Fatalf("second run should be served from cache: %d extra requests", w.Requests.Load()-afterFirst)
	}
	if strings.Join(words(d1), ",") != strings.Join(words(d2), ",") {
		t.Fatal("cached run produced different entries")
	}
}

func TestPresetsCutoff(t *testing.T) {
	p, err := presets.Load("")
	if err != nil {
		t.Fatal(err)
	}
	b, err := p.Book("Rhythm of War")
	if err != nil {
		t.Fatal(err)
	}
	cutoff, _ := p.Cutoff(b, -1)
	if cutoff.Format("2006-01-02") != "2020-11-14" {
		t.Fatalf("default buffer should give 2020-11-14, got %s", cutoff.Format("2006-01-02"))
	}
	cutoff, _ = p.Cutoff(b, 30)
	if cutoff.Format("2006-01-02") != "2020-10-18" {
		t.Fatalf("30-day buffer gave %s", cutoff.Format("2006-01-02"))
	}
	if _, err := p.Universe("stormlight"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Book("not-a-book"); err == nil {
		t.Fatal("expected an error for an unknown book")
	}
}

func TestBuildDictionaryEndToEnd(t *testing.T) {
	Quiet = true
	w := roshar()
	srv := w.Server()
	defer srv.Close()
	out := t.TempDir()
	d, err := BuildDictionary(srv.URL, GeneratorOptions{
		Name: "stormlight-before-row", Output: out, Format: "df", Depth: 1,
		Dated: &DatedOptions{Cutoff: rowCutoff, Categories: []string{"Stormlight Archive"}, CategoryDepth: 1,
			Client: wikibot.ClientOptions{Rate: 1000}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Lang != "en" || len(d.Lexicon) != 4 {
		t.Fatalf("unexpected dict: lang %q, %d entries", d.Lang, len(d.Lexicon))
	}
	data, err := os.ReadFile(filepath.Join(out, "stormlight-before-row.df"))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("\n%s", data)
	if strings.Contains(string(data), "SPOILER") || strings.Contains(string(data), "Stormblessed") || strings.Contains(string(data), "Spoiler Name") {
		t.Fatalf("spoiler leaked into output:\n%s", data)
	}
}
