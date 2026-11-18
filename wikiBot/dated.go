package wikiBot

import (
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Revision timestamps in the MediaWiki API.
const apiTimestamp = "2006-01-02T15:04:05Z"

// DatedPage is a page as it stood just before a cutoff.
type DatedPage struct {
	// Current title of the page.
	Title string
	// False when the page had no revision before the cutoff (it didn't exist
	// yet) or doesn't exist at all.
	Existed   bool
	RevId     int
	Timestamp time.Time
	Content   string
	// Titles of pages that redirect here today. Each must be checked at the
	// cutoff separately before being trusted.
	CurrentRedirects []string
}

type v2Response struct {
	Continue map[string]string `json:"continue"`
	Query    struct {
		Pages []struct {
			PageId    int    `json:"pageid"`
			Title     string `json:"title"`
			Missing   bool   `json:"missing"`
			Invalid   bool   `json:"invalid"`
			Revisions []struct {
				RevId     int    `json:"revid"`
				Timestamp string `json:"timestamp"`
				Slots     struct {
					Main struct {
						Content string `json:"content"`
					} `json:"main"`
				} `json:"slots"`
				// Pre-1.32 wikis without slots.
				Content string `json:"content"`
			} `json:"revisions"`
			Redirects []struct {
				Title string `json:"title"`
			} `json:"redirects"`
		} `json:"pages"`
		AllPages []struct {
			Title string `json:"title"`
		} `json:"allpages"`
		CategoryMembers []struct {
			Ns    int    `json:"ns"`
			Title string `json:"title"`
		} `json:"categorymembers"`
		LogEvents []struct {
			Title     string `json:"title"`
			Timestamp string `json:"timestamp"`
			Params    struct {
				TargetTitle string `json:"target_title"`
			} `json:"params"`
		} `json:"logevents"`
	} `json:"query"`
}

// FetchDatedPage returns the newest revision of title made before cutoff.
// MediaWiki only allows rvstart for a single page per request.
func FetchDatedPage(c *Client, title string, cutoff time.Time, withRedirects bool) (DatedPage, error) {
	params := url.Values{}
	params.Set("action", "query")
	params.Set("titles", title)
	params.Set("prop", "revisions")
	params.Set("rvprop", "ids|timestamp|content")
	params.Set("rvslots", "main")
	params.Set("rvlimit", "1")
	params.Set("rvdir", "older")
	params.Set("rvstart", cutoff.UTC().Format(apiTimestamp))
	if withRedirects {
		params.Set("prop", "revisions|redirects")
		params.Set("rdprop", "title")
		params.Set("rdnamespace", "0")
		params.Set("rdlimit", "max")
	}

	var res v2Response
	if err := c.Get(params, &res); err != nil {
		return DatedPage{}, err
	}

	page := DatedPage{Title: title}
	if len(res.Query.Pages) == 0 {
		return page, nil
	}
	p := res.Query.Pages[0]
	page.Title = p.Title
	for _, r := range p.Redirects {
		page.CurrentRedirects = append(page.CurrentRedirects, r.Title)
	}
	if p.Missing || p.Invalid || len(p.Revisions) == 0 {
		return page, nil
	}
	rev := p.Revisions[0]
	ts, err := time.Parse(apiTimestamp, rev.Timestamp)
	// Belt and braces: never trust a revision at or after the cutoff.
	if err != nil || !ts.Before(cutoff) {
		return page, nil
	}
	page.Existed = true
	page.RevId = rev.RevId
	page.Timestamp = ts
	page.Content = rev.Slots.Main.Content
	if page.Content == "" {
		page.Content = rev.Content
	}
	return page, nil
}

// ListArticles returns the title of every non-redirect article (namespace 0).
func ListArticles(c *Client) ([]string, error) {
	titles := []string{}
	params := url.Values{}
	params.Set("action", "query")
	params.Set("list", "allpages")
	params.Set("apnamespace", "0")
	params.Set("apfilterredir", "nonredirects")
	params.Set("aplimit", "max")
	for {
		var res v2Response
		if err := c.Get(params, &res); err != nil {
			return nil, err
		}
		for _, p := range res.Query.AllPages {
			titles = append(titles, p.Title)
		}
		next := res.Continue["apcontinue"]
		if next == "" {
			return titles, nil
		}
		params.Set("apcontinue", next)
		params.Set("continue", res.Continue["continue"])
	}
}

const categoryNs = 14

// WalkCategories returns the articles in the given root categories and their
// subcategories down to depth levels, skipping anything in exclude.
func WalkCategories(c *Client, roots []string, depth int, exclude []string) ([]string, error) {
	skip := map[string]bool{}
	for _, e := range exclude {
		skip[NormalizeTitle(e)] = true
		skip[NormalizeTitle(withCategoryPrefix(e))] = true
	}

	seenCats := map[string]bool{}
	seenPages := map[string]bool{}
	titles := []string{}

	type item struct {
		cat   string
		level int
	}
	queue := []item{}
	for _, r := range roots {
		queue = append(queue, item{withCategoryPrefix(r), 0})
	}

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		key := NormalizeTitle(cur.cat)
		if seenCats[key] || skip[key] {
			continue
		}
		seenCats[key] = true

		params := url.Values{}
		params.Set("action", "query")
		params.Set("list", "categorymembers")
		params.Set("cmtitle", cur.cat)
		params.Set("cmnamespace", "0|14")
		params.Set("cmlimit", "max")
		for {
			var res v2Response
			if err := c.Get(params, &res); err != nil {
				return nil, err
			}
			for _, m := range res.Query.CategoryMembers {
				t := NormalizeTitle(m.Title)
				if skip[t] {
					continue
				}
				if m.Ns == categoryNs {
					if cur.level < depth {
						queue = append(queue, item{m.Title, cur.level + 1})
					}
					continue
				}
				if !seenPages[t] {
					seenPages[t] = true
					titles = append(titles, m.Title)
				}
			}
			next := res.Continue["cmcontinue"]
			if next == "" {
				break
			}
			params.Set("cmcontinue", next)
			params.Set("continue", res.Continue["continue"])
		}
	}
	return titles, nil
}

func withCategoryPrefix(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(strings.ToLower(s), "category:") {
		return s
	}
	return "Category:" + s
}

// Move is a page rename recorded in the move log.
type Move struct {
	From, To  string
	Timestamp time.Time
}

// MovesSince returns every article move made at or after cutoff.
func MovesSince(c *Client, cutoff time.Time) ([]Move, error) {
	moves := []Move{}
	params := url.Values{}
	params.Set("action", "query")
	params.Set("list", "logevents")
	params.Set("letype", "move")
	params.Set("lenamespace", "0")
	params.Set("leprop", "title|timestamp|details")
	params.Set("ledir", "newer")
	params.Set("lestart", cutoff.UTC().Format(apiTimestamp))
	params.Set("lelimit", "max")
	for {
		var res v2Response
		if err := c.Get(params, &res); err != nil {
			return nil, err
		}
		for _, e := range res.Query.LogEvents {
			ts, err := time.Parse(apiTimestamp, e.Timestamp)
			if err != nil || e.Params.TargetTitle == "" {
				continue
			}
			moves = append(moves, Move{From: e.Title, To: e.Params.TargetTitle, Timestamp: ts})
		}
		next := res.Continue["lecontinue"]
		if next == "" {
			return moves, nil
		}
		params.Set("lecontinue", next)
		params.Set("continue", res.Continue["continue"])
	}
}

// TitleHistory answers "what was this page called at the cutoff?" by
// replaying later moves backwards.
type TitleHistory struct {
	byTarget map[string][]Move
}

func NewTitleHistory(moves []Move) TitleHistory {
	h := TitleHistory{byTarget: map[string][]Move{}}
	for _, m := range moves {
		k := NormalizeTitle(m.To)
		h.byTarget[k] = append(h.byTarget[k], m)
	}
	for k := range h.byTarget {
		ms := h.byTarget[k]
		sort.Slice(ms, func(i, j int) bool { return ms[i].Timestamp.After(ms[j].Timestamp) })
	}
	return h
}

// TitleAtCutoff returns the title a page had before any of the recorded moves.
func (h TitleHistory) TitleAtCutoff(current string) string {
	title := current
	bound := time.Time{} // zero = no upper bound yet
	for i := 0; i < 100; i++ {
		var found *Move
		for _, m := range h.byTarget[NormalizeTitle(title)] {
			if bound.IsZero() || m.Timestamp.Before(bound) {
				m := m
				found = &m
				break
			}
		}
		if found == nil {
			break
		}
		title = found.From
		bound = found.Timestamp
	}
	return title
}

var redirectRe = regexp.MustCompile(`(?i)^\s*#\s*REDIRECT\s*:?\s*\[\[([^\]|#]+)`)

// RedirectTarget returns the target if wikitext is a redirect.
func RedirectTarget(content string) (string, bool) {
	m := redirectRe.FindStringSubmatch(content)
	if m == nil {
		return "", false
	}
	return strings.TrimSpace(m[1]), true
}

// NormalizeTitle makes titles comparable the way MediaWiki does: underscores
// are spaces and the first letter is case-insensitive.
func NormalizeTitle(t string) string {
	t = strings.TrimSpace(strings.ReplaceAll(t, "_", " "))
	t = strings.Join(strings.Fields(t), " ")
	if t == "" {
		return t
	}
	r := []rune(t)
	return strings.ToUpper(string(r[0])) + string(r[1:])
}
