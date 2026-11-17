package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	pprof "runtime/pprof"
	"strings"
	"time"

	"github.com/runik-3/builder/dict"
	"github.com/runik-3/builder/presets"
	"github.com/runik-3/builder/wasm"
	wikibot "github.com/runik-3/builder/wikiBot"
)

type listFlag []string

func (l *listFlag) String() string { return strings.Join(*l, ",") }
func (l *listFlag) Set(v string) error {
	for _, s := range strings.Split(v, ",") {
		if s = strings.TrimSpace(s); s != "" {
			*l = append(*l, s)
		}
	}
	return nil
}

func main() {
	// if running from wasm context, block main to keep program long lived
	if runtime.GOOS == "js" && runtime.GOARCH == "wasm" {
		wasm.RegisterWasmFuncs()
		// no reader for channel, blocks execution so JS can call to Go funcs
		keepAlive := make(chan int)
		<-keepAlive
		return
	}

	entryLimit := flag.Int("l", 10000, "The maximum number of entries in the dictionary.")
	depth := flag.Int("d", 1, "How many sentences make up each definition.")
	name := flag.String("n", "", "The file name of the generated dictionary (extension added automatically).")
	output := flag.String("o", "", "The output directory where generated dictionary will be written. If not passed in, no file will be generated (preferred behaviour when calling builder as module).")
	format := flag.String("f", "json", "Format of the output file.")
	cpuprofile := flag.String("cpuprofile", "", "write cpu profile to file")

	// Spoiler-free options
	universe := flag.String("universe", "", "Limit the dictionary to a universe from the presets (eg. stormlight, cosmere). See `builder books`.")
	book := flag.String("book", "", "The book you are about to start. Wiki content is taken from just before its release (eg. rhythm-of-war).")
	before := flag.String("before", "", "Use the wiki as it was before this date (YYYY-MM-DD). Overrides -book.")
	buffer := flag.Int("buffer", -1, "Days before the book's release to cut off. Defaults to the presets' value (3).")
	presetsPath := flag.String("presets", "", "Path to a presets JSON file. Defaults to the built-in Coppermind presets.")
	var categories, exclude listFlag
	flag.Var(&categories, "category", "Category to build from, instead of a preset universe. Repeat or comma-separate for several.")
	catDepth := flag.Int("category-depth", 2, "Levels of subcategories to include with -category.")
	flag.Var(&exclude, "exclude", "Page or category titles to leave out. Repeat or comma-separate for several.")
	noSynonyms := flag.Bool("no-synonyms", false, "Skip alternative names (redirects). About half as many requests.")
	rate := flag.Float64("rate", 2, "Maximum requests per second to the wiki.")
	workers := flag.Int("workers", 4, "Parallel requests in flight.")
	cacheDir := flag.String("cache", defaultCacheDir(), "Directory to cache wiki responses in, so reruns are fast. Empty to disable.")
	contact := flag.String("contact", "", "Your contact info (email or URL), added to the User-Agent so wiki admins can reach you.")

	flag.Usage = usage
	args := parseInterspersed()

	if *cpuprofile != "" {
		f, err := os.Create(*cpuprofile)
		if err != nil {
			log.Fatal(err)
		}
		pprof.StartCPUProfile(f)
		defer pprof.StopCPUProfile()
	}

	if len(args) < 1 {
		usage()
		os.Exit(1)
	}
	command := args[0]

	switch command {
	case "books":
		p, err := presets.Load(*presetsPath)
		if err != nil {
			log.Fatal(err)
		}
		p.Print(os.Stdout)

	case "generate":
		options := dict.GeneratorOptions{
			Name:         *name,
			Output:       *output,
			Format:       *format,
			Depth:        *depth,
			EntryLimit:   *entryLimit,
			ProgressHook: printProgress,
		}

		wikiUrl := ""
		if len(args) > 1 {
			wikiUrl = args[1]
		}

		spoilerFree := *universe != "" || *book != "" || *before != "" || len(categories) > 0
		if spoilerFree {
			p, err := presets.Load(*presetsPath)
			if err != nil {
				log.Fatal(err)
			}
			dated := &dict.DatedOptions{
				Categories:    categories,
				CategoryDepth: *catDepth,
				Exclude:       exclude,
				SkipRedirects: *noSynonyms,
				Workers:       *workers,
				Client: wikibot.ClientOptions{
					Rate:     *rate,
					CacheDir: *cacheDir,
					Contact:  *contact,
				},
			}

			nameParts := []string{}
			if *universe != "" {
				u, err := p.Universe(*universe)
				if err != nil {
					log.Fatal(err)
				}
				if len(categories) == 0 {
					dated.Categories = u.Categories
					dated.CategoryDepth = u.Depth
				}
				dated.Exclude = append(dated.Exclude, u.Exclude...)
				nameParts = append(nameParts, strings.ToLower(*universe))
			}

			switch {
			case *before != "":
				cutoff, err := presets.ParseDate(*before)
				if err != nil {
					log.Fatal(err)
				}
				dated.Cutoff = cutoff
				nameParts = append(nameParts, "before-"+*before)
			case *book != "":
				b, err := p.Book(*book)
				if err != nil {
					log.Fatal(err)
				}
				if *universe != "" && len(p.Universes[strings.ToLower(*universe)].Categories) > 0 && b.Series != strings.ToLower(*universe) {
					log.Printf("⚠️  %s is a %s book, but you're building the %s universe. Continuing anyway.", b.Title, b.Series, *universe)
				}
				cutoff, err := p.Cutoff(b, *buffer)
				if err != nil {
					log.Fatal(err)
				}
				dated.Cutoff = cutoff
				nameParts = append(nameParts, "before-"+b.Id)
			default:
				// A universe without a date: the current wiki, scoped.
				dated.Cutoff = time.Now().UTC().Add(time.Hour)
			}

			if wikiUrl == "" {
				wikiUrl = p.Wiki
			}
			if options.Name == "" && len(nameParts) > 0 {
				options.Name = strings.Join(nameParts, "-")
			}
			options.Dated = dated
			fmt.Fprintf(os.Stderr, "🔒 Using %s as it was before %s\n", wikiUrl, dated.Cutoff.Format("2006-01-02"))
		}

		if wikiUrl == "" {
			log.Fatal("You must provide a wiki url, eg. builder generate https://coppermind.net")
		}
		_, err := dict.BuildDictionary(wikiUrl, options)
		if err != nil {
			log.Fatalf("There was an error building the dictionary:\n%s", err.Error())
		}

	case "info":
		if len(args) < 2 {
			log.Fatal("You must provide a wiki url.")
		}
		err := wikibot.PrintWikiDetails(args[1])
		if err != nil {
			log.Fatalf("There was an error retrieving wiki info:\n%s", err.Error())
		}
	default:
		log.Fatalf("%s is not a valid command. See help for more options.", args[0])
	}
}

// parseInterspersed lets flags come before or after the command and url.
func parseInterspersed() []string {
	positional := []string{}
	rest := os.Args[1:]
	for {
		if err := flag.CommandLine.Parse(rest); err != nil {
			os.Exit(2)
		}
		rest = flag.Args()
		if len(rest) == 0 {
			return positional
		}
		positional = append(positional, rest[0])
		rest = rest[1:]
	}
}

func defaultCacheDir() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "runik-builder")
}

func usage() {
	out := flag.CommandLine.Output()
	fmt.Fprintf(out, `Usage:
  builder [flags] generate [wiki_url]
  builder [flags] info wiki_url
  builder [-presets file] books

Spoiler-free example (Stormlight only, wiki as of just before Rhythm of War):
  builder -universe stormlight -book rhythm-of-war -o ./out generate

Flags:
`)
	flag.PrintDefaults()
}

var lastProgress time.Time

func printProgress(current int, total int) {
	if current == total || time.Since(lastProgress) > 2*time.Second {
		lastProgress = time.Now()
		fmt.Fprintf(os.Stderr, "%d/%d articles processed\n", current, total)
	}
}
