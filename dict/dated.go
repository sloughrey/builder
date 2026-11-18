package dict

import (
	"fmt"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/runik-3/builder/internal/wikitext"
	wikibot "github.com/runik-3/builder/wikiBot"
)

// DatedOptions turn on spoiler-free generation: every definition comes from
// the newest revision made before Cutoff.
type DatedOptions struct {
	Cutoff time.Time
	// Root categories to build from. Empty means every article on the wiki.
	Categories    []string
	CategoryDepth int
	Exclude       []string
	// Leave out alternative names (redirects), halving the request count.
	SkipRedirects bool
	// Parallel requests in flight; the client's rate limit still applies.
	Workers int
	Client  wikibot.ClientOptions
}

// Stats summarises a dated build, for the CLI report.
type DatedStats struct {
	Candidates     int
	NotYetCreated  int
	Unparsable     int
	Entries        int
	Synonyms       int
	Renamed        int
	LateRedirects  int
	FailedRequests int
}

type datedResult struct {
	page wikibot.DatedPage
	err  error
}

// fetchAll fetches titles at the cutoff with a small worker pool, calling
// progress after each page.
func fetchAll(c *wikibot.Client, titles []string, opts DatedOptions, withRedirects bool, progress func(int)) []datedResult {
	workers := opts.Workers
	if workers <= 0 {
		workers = 4
	}
	results := make([]datedResult, len(titles))
	jobs := make(chan int)
	var wg sync.WaitGroup
	var mu sync.Mutex
	done := 0
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				p, err := wikibot.FetchDatedPage(c, titles[i], opts.Cutoff, withRedirects)
				results[i] = datedResult{p, err}
				mu.Lock()
				done++
				if progress != nil {
					progress(done)
				}
				mu.Unlock()
			}
		}()
	}
	for i := range titles {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	return results
}

// GenerateDatedDefinitions builds the lexicon from each page as it stood before
// opts.Cutoff. Pages created after the cutoff are left out, redirects only
// count if they existed by then, and pages renamed later keep their old names.
func (dict *Dict) GenerateDatedDefinitions(wiki wikibot.WikiDetails, options GeneratorOptions, opts DatedOptions) (DatedStats, error) {
	stats := DatedStats{}
	c := wikibot.NewClient(wiki.ApiUrl, opts.Client)

	logf("⏳ Collecting pages...\n")
	var candidates []string
	var err error
	if len(opts.Categories) > 0 {
		candidates, err = wikibot.WalkCategories(c, opts.Categories, opts.CategoryDepth, opts.Exclude)
	} else {
		candidates, err = wikibot.ListArticles(c)
		candidates = withoutExcluded(candidates, opts.Exclude)
	}
	if err != nil {
		return stats, fmt.Errorf("could not list pages: %w", err)
	}
	if len(candidates) == 0 {
		return stats, fmt.Errorf("no pages found; check the category names")
	}
	if options.EntryLimit > 0 && len(candidates) > options.EntryLimit {
		logf("⚠️  %d pages found, only the first %d will be used (raise -l)\n", len(candidates), options.EntryLimit)
		candidates = candidates[:options.EntryLimit]
	}
	stats.Candidates = len(candidates)

	history := wikibot.NewTitleHistory(nil)
	moves, err := wikibot.MovesSince(c, opts.Cutoff)
	if err != nil {
		logf("⚠️  Could not read the move log, renamed pages will show their current names: %s\n", err)
	} else {
		history = wikibot.NewTitleHistory(moves)
	}

	logf("⏳ Fetching %d pages as of %s...\n", len(candidates), opts.Cutoff.Format("2006-01-02"))
	results := fetchAll(c, candidates, opts, !opts.SkipRedirects, func(done int) {
		if options.ProgressHook != nil {
			options.ProgressHook(done, len(candidates))
		}
	})

	// Keyed by the page's title at the cutoff.
	articles := map[string]wikibot.DatedPage{}
	articleTitle := map[string]string{}
	redirectsTo := map[string]string{}
	redirectTitle := map[string]string{}
	seen := map[string]bool{}
	extra := []string{}

	addPage := func(p wikibot.DatedPage) {
		then := history.TitleAtCutoff(p.Title)
		key := wikibot.NormalizeTitle(then)
		if target, ok := wikibot.RedirectTarget(p.Content); ok {
			redirectsTo[key] = wikibot.NormalizeTitle(target)
			redirectTitle[key] = then
			return
		}
		if then != p.Title {
			stats.Renamed++
		}
		articles[key] = p
		articleTitle[key] = then
	}

	for i, r := range results {
		seen[wikibot.NormalizeTitle(candidates[i])] = true
		if r.err != nil {
			stats.FailedRequests++
			logf("⚠️  %s: %s\n", candidates[i], r.err)
			continue
		}
		if !r.page.Existed {
			stats.NotYetCreated++
			continue
		}
		addPage(r.page)
		for _, rd := range r.page.CurrentRedirects {
			k := wikibot.NormalizeTitle(rd)
			if !seen[k] {
				seen[k] = true
				extra = append(extra, rd)
			}
		}
	}

	if len(extra) > 0 {
		logf("⏳ Checking %d alternative names as of the cutoff...\n", len(extra))
		for _, r := range fetchAll(c, extra, opts, false, nil) {
			if r.err != nil {
				stats.FailedRequests++
				continue
			}
			if !r.page.Existed {
				stats.LateRedirects++
				continue
			}
			addPage(r.page)
		}
	}

	// Resolve redirects (following short chains) onto articles.
	synonyms := map[string][]string{}
	for from, to := range redirectsTo {
		target := to
		for hops := 0; hops < 5; hops++ {
			next, isRedirect := redirectsTo[target]
			if !isRedirect {
				break
			}
			target = next
		}
		if _, ok := articles[target]; ok {
			synonyms[target] = append(synonyms[target], wikitext.CleanWord(redirectTitle[from]))
		}
	}

	keys := make([]string, 0, len(articles))
	for k := range articles {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		p := articles[k]
		word := wikitext.CleanWord(articleTitle[k])
		def, err := wikitext.ParseDefinition(p.Content, options.Depth)
		if err != nil || def == "" {
			stats.Unparsable++
			continue
		}
		syns := uniqueSynonyms(word, synonyms[k])
		stats.Synonyms += len(syns)
		dict.Lexicon.Add(Entry{Word: word, Definition: def, Synonyms: syns})
	}
	stats.Entries = len(dict.Lexicon)
	return stats, nil
}

func uniqueSynonyms(word string, syns []string) []string {
	out := []string{}
	seen := map[string]bool{word: true}
	sort.Strings(syns)
	for _, s := range syns {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func withoutExcluded(titles []string, exclude []string) []string {
	if len(exclude) == 0 {
		return titles
	}
	skip := map[string]bool{}
	for _, e := range exclude {
		skip[wikibot.NormalizeTitle(e)] = true
	}
	out := titles[:0]
	for _, t := range titles {
		if !skip[wikibot.NormalizeTitle(t)] {
			out = append(out, t)
		}
	}
	return out
}

// Quiet silences progress logging (used by tests).
var Quiet = false

func logf(format string, args ...any) {
	if !Quiet {
		fmt.Fprintf(os.Stderr, format, args...)
	}
}

func (s DatedStats) Print() {
	logf("\n📖 %d entries, %d alternative names\n", s.Entries, s.Synonyms)
	logf("   %d pages checked, %d skipped because they were created after the cutoff\n", s.Candidates, s.NotYetCreated)
	if s.LateRedirects > 0 {
		logf("   %d alternative names skipped because they were added after the cutoff\n", s.LateRedirects)
	}
	if s.Renamed > 0 {
		logf("   %d pages shown under the name they had at the cutoff\n", s.Renamed)
	}
	if s.Unparsable > 0 {
		logf("   %d pages had no usable first sentence\n", s.Unparsable)
	}
	if s.FailedRequests > 0 {
		logf("   ⚠️  %d requests failed; rerun with the same -cache to retry only those\n", s.FailedRequests)
	}
}
