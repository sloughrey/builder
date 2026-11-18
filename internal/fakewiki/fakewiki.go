// Package fakewiki is a tiny in-memory MediaWiki API for tests. It answers
// the handful of queries the builder makes, including dated revision lookups,
// so spoiler filtering can be tested without touching a real wiki.
package fakewiki

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

type Rev struct {
	Id      int
	At      string // 2006-01-02
	Content string
}

type Page struct {
	Title      string
	Revs       []Rev // oldest first
	Categories []string
}

type Move struct {
	From, To, At string
}

type Wiki struct {
	Pages []Page
	Moves []Move
	// Categories whose subcategories are listed: parent -> children.
	Subcats map[string][]string
	// Fail this many requests with 429 before answering.
	FailFirst int32
	Requests  atomic.Int32
}

func day(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

func isRedirect(p Page) (string, bool) {
	c := p.Revs[len(p.Revs)-1].Content
	if strings.HasPrefix(c, "#REDIRECT [[") {
		return strings.TrimSuffix(strings.TrimPrefix(c, "#REDIRECT [["), "]]"), true
	}
	return "", false
}

func (w *Wiki) page(title string) *Page {
	for i := range w.Pages {
		if w.Pages[i].Title == title {
			return &w.Pages[i]
		}
	}
	return nil
}

func (w *Wiki) Server() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(w.handle))
}

func (w *Wiki) handle(rw http.ResponseWriter, r *http.Request) {
	n := w.Requests.Add(1)
	if n <= w.FailFirst {
		rw.WriteHeader(http.StatusTooManyRequests)
		return
	}
	q := r.URL.Query()
	out := map[string]any{}
	query := map[string]any{}
	out["query"] = query

	switch {
	case q.Get("meta") == "siteinfo":
		query["general"] = map[string]any{"mainpage": "Main Page", "sitename": "Fake Wiki", "lang": "en"}
		query["statistics"] = map[string]any{"pages": len(w.Pages), "articles": len(w.Pages)}
	case q.Get("prop") == "langlinks":
		query["pages"] = map[string]any{}
	case q.Get("list") == "allpages":
		titles := []map[string]any{}
		for _, p := range w.Pages {
			if _, rd := isRedirect(p); !rd {
				titles = append(titles, map[string]any{"title": p.Title})
			}
		}
		query["allpages"] = titles
	case q.Get("list") == "categorymembers":
		cat := strings.TrimPrefix(q.Get("cmtitle"), "Category:")
		members := []map[string]any{}
		for _, p := range w.Pages {
			for _, c := range p.Categories {
				if c == cat {
					members = append(members, map[string]any{"ns": 0, "title": p.Title})
				}
			}
		}
		for _, sub := range w.Subcats[cat] {
			members = append(members, map[string]any{"ns": 14, "title": "Category:" + sub})
		}
		// Page through results two at a time to exercise continuation.
		start := 0
		if c := q.Get("cmcontinue"); c != "" {
			start = int(c[0] - '0')
		}
		end := start + 2
		if end < len(members) {
			out["continue"] = map[string]any{"cmcontinue": string(rune('0' + end)), "continue": "-||"}
		} else {
			end = len(members)
		}
		query["categorymembers"] = members[start:end]
	case q.Get("list") == "logevents":
		since := day(q.Get("lestart")[:10])
		events := []map[string]any{}
		for _, m := range w.Moves {
			if !day(m.At).Before(since) {
				events = append(events, map[string]any{
					"title":     m.From,
					"timestamp": m.At + "T12:00:00Z",
					"params":    map[string]any{"target_ns": 0, "target_title": m.To},
				})
			}
		}
		query["logevents"] = events
	case strings.HasPrefix(q.Get("prop"), "revisions"):
		title := q.Get("titles")
		p := w.page(title)
		if p == nil {
			query["pages"] = []map[string]any{{"title": title, "missing": true}}
			break
		}
		start, _ := time.Parse("2006-01-02T15:04:05Z", q.Get("rvstart"))
		page := map[string]any{"pageid": 1, "title": p.Title}
		revs := append([]Rev{}, p.Revs...)
		sort.Slice(revs, func(i, j int) bool { return revs[i].At > revs[j].At })
		for _, rv := range revs {
			at := day(rv.At)
			if !at.After(start) {
				page["revisions"] = []map[string]any{{
					"revid":     rv.Id,
					"timestamp": rv.At + "T12:00:00Z",
					"slots":     map[string]any{"main": map[string]any{"content": rv.Content}},
				}}
				break
			}
		}
		if strings.Contains(q.Get("prop"), "redirects") {
			rds := []map[string]any{}
			for _, other := range w.Pages {
				if target, ok := isRedirect(other); ok && target == p.Title {
					rds = append(rds, map[string]any{"title": other.Title})
				}
			}
			if len(rds) > 0 {
				page["redirects"] = rds
			}
		}
		query["pages"] = []map[string]any{page}
	default:
		rw.WriteHeader(http.StatusBadRequest)
		return
	}
	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(out)
}

// R builds a revision.
func R(id int, at, content string) Rev { return Rev{Id: id, At: at, Content: content} }
