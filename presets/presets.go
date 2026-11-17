// Package presets maps human-friendly universe and book names onto the
// categories and cutoff dates used to build spoiler-free dictionaries.
package presets

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"
)

//go:embed coppermind.json
var defaultPresets []byte

const DateLayout = "2006-01-02"

type Universe struct {
	Title string `json:"title"`
	// Root categories (without the "Category:" prefix). Empty means the whole
	// wiki.
	Categories []string `json:"categories"`
	// How many levels of subcategories to walk below each root category.
	Depth int `json:"depth"`
	// Page or category titles to leave out.
	Exclude []string `json:"exclude"`
}

type Book struct {
	Id       string `json:"id"`
	Title    string `json:"title"`
	Series   string `json:"series"`
	Released string `json:"released"`
}

func (b Book) ReleaseDate() (time.Time, error) {
	return time.Parse(DateLayout, b.Released)
}

type Presets struct {
	Wiki       string              `json:"wiki"`
	BufferDays int                 `json:"bufferDays"`
	Universes  map[string]Universe `json:"universes"`
	Books      []Book              `json:"books"`
}

// Load reads presets from path, or the embedded Coppermind presets when path
// is empty.
func Load(path string) (Presets, error) {
	data := defaultPresets
	if path != "" {
		var err error
		data, err = os.ReadFile(path)
		if err != nil {
			return Presets{}, fmt.Errorf("could not read presets file: %w", err)
		}
	}
	return Parse(data)
}

func Parse(data []byte) (Presets, error) {
	var p Presets
	if err := json.Unmarshal(data, &p); err != nil {
		return Presets{}, fmt.Errorf("invalid presets file: %w", err)
	}
	for _, b := range p.Books {
		if _, err := b.ReleaseDate(); err != nil {
			return Presets{}, fmt.Errorf("book %q has an invalid release date %q (want YYYY-MM-DD)", b.Id, b.Released)
		}
	}
	return p, nil
}

func normalizeId(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

func (p Presets) Book(id string) (Book, error) {
	want := normalizeId(id)
	for _, b := range p.Books {
		if normalizeId(b.Id) == want || normalizeId(b.Title) == want {
			return b, nil
		}
	}
	return Book{}, fmt.Errorf("unknown book %q; run `builder books` to list the presets", id)
}

func (p Presets) Universe(id string) (Universe, error) {
	u, ok := p.Universes[normalizeId(id)]
	if !ok {
		return Universe{}, fmt.Errorf("unknown universe %q; run `builder books` to list the presets", id)
	}
	return u, nil
}

// Cutoff returns the moment before which wiki revisions are kept for a reader
// starting `book`: its release date minus bufferDays. A negative bufferDays
// uses the presets' default.
func (p Presets) Cutoff(book Book, bufferDays int) (time.Time, error) {
	released, err := book.ReleaseDate()
	if err != nil {
		return time.Time{}, err
	}
	if bufferDays < 0 {
		bufferDays = p.BufferDays
	}
	return released.AddDate(0, 0, -bufferDays), nil
}

// ParseDate parses a YYYY-MM-DD cutoff date (midnight UTC).
func ParseDate(s string) (time.Time, error) {
	t, err := time.Parse(DateLayout, strings.TrimSpace(s))
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid date %q (want YYYY-MM-DD)", s)
	}
	return t, nil
}

// Print writes a readable list of universes and books.
func (p Presets) Print(w io.Writer) {
	fmt.Fprintf(w, "Presets for %s\n\nUniverses (-universe):\n", p.Wiki)
	ids := make([]string, 0, len(p.Universes))
	for id := range p.Universes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		fmt.Fprintf(w, "  %-12s %s\n", id, p.Universes[id].Title)
	}

	fmt.Fprintf(w, "\nBooks (-book), default cutoff %d days before release:\n", p.BufferDays)
	books := append([]Book{}, p.Books...)
	sort.SliceStable(books, func(i, j int) bool {
		if books[i].Series != books[j].Series {
			return books[i].Series < books[j].Series
		}
		return books[i].Released < books[j].Released
	})
	series := ""
	for _, b := range books {
		if b.Series != series {
			series = b.Series
			fmt.Fprintf(w, "  [%s]\n", series)
		}
		fmt.Fprintf(w, "    %-20s %s  %s\n", b.Id, b.Released, b.Title)
	}
}
