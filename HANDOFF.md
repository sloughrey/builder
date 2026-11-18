# Handoff: spoiler-free Runik dictionaries

Fork of [Runik-3/builder](https://github.com/Runik-3/builder) (Go CLI that turns a
MediaWiki wiki into an e-reader dictionary). This branch adds **spoiler-free
builds**: every definition comes from the wiki as it stood just before a chosen
book's release, scoped to a chosen universe (Cosmere, Stormlight, Mistborn, ...).

The owner reads epic fantasy on a **Kobo** and uses the **Runik desktop app**
([Runik-3/core](https://github.com/Runik-3/core)) to install dictionaries.
First target: the Stormlight Archive on the [Coppermind](https://coppermind.net)
while reading book 4 (Rhythm of War), so the wiki must stop just before book 4.

Plan doc (design, phases, risks): https://claude.ai/artifact/GzhnJHh3gjH3GoipRPrdWB

## Status

- Branch `spoiler-cutoff`, one commit (`20c6136`) on top of upstream `main`.
  Remotes: `upstream` = Runik-3/builder; `origin` = the owner's fork (if added).
- `go vet ./...` clean, all tests pass, native and `GOOS=js GOARCH=wasm` builds work.
- **Not yet run against the real Coppermind.** The previous environment's
  network blocked it. Everything was tested against an in-memory fake wiki.

## Why not the Coppermind "Time Machine"

The Coppermind has a [Time Machine](https://github.com/17thshard/mediawiki-time-machine)
that sets a `coppermind_mw_timemachine-date` cookie. It only hooks page
*rendering*, search and category pages; it does not affect `api.php`, which is
what Runik uses. So the cookie can't be used. Instead we ask the standard API
for each page's newest revision before the cutoff
(`prop=revisions&rvstart=<cutoff>&rvdir=older&rvlimit=1`), which is what the
Time Machine does internally and works on any MediaWiki wiki (Fandom too).

## Usage

```sh
go build -o builder.exe .
./builder.exe books                                   # list universes and books
./builder.exe -universe stormlight -book rhythm-of-war -o ./out generate
./builder.exe -universe cosmere -book rhythm-of-war -buffer 30 -o ./out generate
./builder.exe -before 2021-03-01 -category "Characters" -o ./out generate https://malazan.fandom.com
```

- Cutoff = book release date minus `-buffer` days (default **3**, the owner's
  choice, set in `presets/coppermind.json` `bufferDays`). `-before YYYY-MM-DD`
  overrides it.
- With presets, the wiki URL is optional (defaults to the presets' `wiki`).
- Default output name: `<universe>-before-<book>` (e.g. `stormlight-before-rhythm-of-war.json`).
- Flags may come before or after the command (`parseInterspersed` in `main.go`).
- Output is Runik's normal `json` (or `df`). Install path for the owner: copy the
  JSON into the Runik app's dictionaries folder and install to the Kobo from the
  app (it converts with dictutil).

## How it works (`dict/dated.go` → `GenerateDatedDefinitions`)

1. **Candidates**: `WalkCategories` (`list=categorymembers`, ns 0|14, BFS to
   `depth`, cycle guard, `exclude`) for a universe; `ListArticles`
   (`list=allpages`, non-redirects) for the whole wiki. Truncated to `-l`.
2. **Renames**: `MovesSince` reads the move log from the cutoff onward;
   `TitleHistory.TitleAtCutoff` replays moves backwards so a page renamed later
   (the new name can itself be a spoiler) keeps the name it had at the cutoff.
3. **Dated fetch**: `FetchDatedPage` per title (MediaWiki only allows `rvstart`
   for one page per request), via a worker pool (`-workers`, default 4) sharing
   one rate limiter (`-rate`, default 2 req/s). Pages with no revision before
   the cutoff are dropped. A revision at/after the cutoff is rejected as a
   safety net.
4. **Redirects (synonyms)**: the dated fetch also returns *today's* redirects
   (`prop=redirects`). Each is fetched again at the cutoff; it only becomes a
   synonym if it existed then, and it points at whatever it targeted then
   (chains followed up to 5 hops). A redirect that was a full article at the
   cutoff becomes its own entry. `-no-synonyms` skips all this.
5. **Parse**: the existing `wikitext.ParseDefinition` / `CleanWord`, then
   `Lexicon.Add`. Stats printed at the end (`DatedStats.Print`).

## Files changed / added

| File | What |
| --- | --- |
| `main.go` | New flags (`-universe -book -before -buffer -presets -category -category-depth -exclude -no-synonyms -rate -workers -cache -contact`), `books` command, interspersed flag parsing, default cache dir (`os.UserCacheDir()/runik-builder`) |
| `dict/builder.go` | `GeneratorOptions.Dated *DatedOptions`; dated path vs. original path |
| `dict/dated.go` (new) | Orchestration described above |
| `wikiBot/client.go` (new) | Rate-limited client: `formatversion=2`, `maxlag=5`, retries with backoff on 429/5xx/network/maxlag (honours `Retry-After`), User-Agent + optional `-contact`, on-disk response cache keyed by sha256 of the request URL |
| `wikiBot/dated.go` (new) | API calls: `FetchDatedPage`, `ListArticles`, `WalkCategories`, `MovesSince`, `TitleHistory`, `RedirectTarget`, `NormalizeTitle` |
| `presets/presets.go`, `presets/coppermind.json` (new) | Embedded presets: universes → categories/depth/exclude; books → series + release date; `Cutoff()` |
| `internal/fakewiki/fakewiki.go` (new) | httptest fake MediaWiki answering every query above, with dated revisions, moves, subcategories, paging and forced 429s |
| `dict/dated_test.go` (new) | Tests: Stormlight scoping, later cutoff, exclude + no-synonyms, retry after 429, cache reuse, presets cutoff, end-to-end `.df` with leak checks |
| `internal/utils/request.go` | Bug fix: JSON decode error returned `err` (nil) instead of `e`; nil check on `http.NewRequest` |
| `README.md` | "Spoiler-free dictionaries" section |

The original allpages path (`wikiBot/batchPages.go`, `GenerateDefinitionsFromWiki`)
is untouched and still used when no spoiler flag is given.

## Next steps

1. **Phase 0: verify against the real Coppermind** (done 2026-11-17)

   Update: `Worldhoppers` is now in the stormlight universe's categories (owner's
   call). Rebuild: 2119 pages, 1448 entries, 563 alternative names; Hoid is in,
   leak check still clean. Mistborn/Elantris/Warbreaker don't have it yet.

   Real build `-universe stormlight -book rhythm-of-war` (cutoff 2020-11-14,
   default rate 2 req/s, ~25 min): 2091 candidate pages, 596 dropped as created
   after the cutoff, 1431 entries, 552 alternative names (128 dropped as added
   after the cutoff), 25 pages under their pre-cutoff name. Output 183 KB JSON.

   Spoiler-safe spot-check (scripts print counts and pass/fail only, never titles
   or text, so the owner isn't spoiled):
   - 10 random entries: the revision the builder used is the same revision the
     Time Machine (cookie `coppermind_mw_timemachine-date=2020-11-14`) renders,
     and 100% of each definition's words appear on the Time Machine page.
   - Every ns-0 page created (397) or moved to a new name (35) between the cutoff
     and 2021-06-01: none is in the dictionary. One created title matched an
     entry only case-insensitively; the entry is from an older page whose title
     differs in case, and its definition matches that page's pre-cutoff revision.
     (MediaWiki titles are case-sensitive after the first letter; compare titles
     exactly, not lowercased, when checking leaks.)

   Also found while doing this:
   - `generate -o <dir>` failed only after the whole build if `<dir>` didn't
     exist; `main.go` now creates it first.
   - On Windows with `core.autocrlf=true`, `TestFormatDictFile` fails because the
     fixture is checked out with CRLF (it's LF in git). Checkout issue, not a code
     bug; a `.gitattributes` with `*.df text eol=lf` would fix it.

   API checks:
   - Endpoint is `https://coppermind.net/w/api.php` (MediaWiki 1.35.14);
     `/api.php` is a 404. Endpoint detection already tries `/w/api.php`.
   - Categories `Stormlight Archive` (2010 pages, 78 subcats), `Mistborn` (991, 34),
     `Elantris` (212, 12), `Warbreaker` (126, 14) all exist. Depth 2 subcats look
     clean: the only cross-series one is `Ghostbloods` (in Stormlight and Mistborn),
     which adds noise but no spoilers, since the text is still dated. No `exclude` needed yet.
   - Gap: worldhoppers (Hoid, ...) aren't under `Stormlight Archive` at any depth;
     they're in `Category:Worldhoppers` (65 pages, no subcats) under `General Cosmere`.
     Roshar's Shards (Honor, Odium, Cultivation) are tagged Stormlight directly.
     Fixed for stormlight by adding `"Worldhoppers"` (see the update above).
   - Book dates match the `Special:TimeMachine` dropdown (it uses release minus 2
     days: RoW 2020-11-15, Oathbringer 2017-11-12, Lost Metal 2022-11-13, WaT
     2024-12-05). The 3-day buffer is one day stricter. The Time Machine warns
     that pages before Words of Radiance are "bad". Wiki history starts 2010-06-13.
   - Anonymous access works for `rvstart` lookups, `list=logevents&letype=move`
     (shape confirmed: `params.target_title` with namespace prefix, plus
     `target_ns`) and `list=allrevisions` (so bulk mode is possible).
   - `prop=revisions|redirects` with `rvlimit=1` returns
     `continue: {rvcontinue, continue: "||redirects"}`; redirects are complete in
     the first response and `rvcontinue` only points at older revisions, so
     `FetchDatedPage` ignoring `continue` is correct.

   Original Phase 0 checklist (all done):
   - `./builder.exe info https://coppermind.net` works (endpoint detection tries `/api.php`, `/w/api.php`, ...).
   - Confirm the real category names and a sensible depth for each universe in
     `presets/coppermind.json`. Only `Category:Stormlight_Archive` is known to
     exist; `Mistborn`, `Elantris`, `Warbreaker` are guesses. Check with
     `api.php?action=query&list=categorymembers&cmtitle=Category:Stormlight_Archive&cmtype=subcat&cmlimit=max`.
     Watch for subcategories that pull in too much (e.g. cross-Cosmere characters) and add them to `exclude`.
   - Check book dates against the dropdown on `Special:TimeMachine`. Current
     values are US first-edition dates written from memory.
   - Confirm anonymous access to `rvstart` lookups and `list=logevents&letype=move`
     (move-log `params.target_title` shape assumed, formatversion 2).
   - Spot-check without spoiling the owner: compare ~10 definitions to the page
     viewed with the Time Machine set to the same date, and confirm pages known
     to be created for book 4 are absent.
2. **Speed (bulk mode)**: whole-Cosmere builds are one request per page plus one
   per redirect. Add a bulk mode: walk `list=allrevisions` backwards from the
   cutoff (`arvdir=older`, `arvlimit=500`, ids/timestamps only), keep the first
   revision seen per page, then fetch content with `revids=` 50 at a time.
   Pick it automatically above ~3,000 candidates. Check anonymous access to
   `allrevisions` first.
3. **Cache keyed by revision ID** so building for a different book reuses
   unchanged pages (the current cache is per request URL, so it only helps
   reruns of the same build and resuming after failures).
4. **Runik app integration (optional)**: in Runik-3/core, add universe and book
   pickers, pass `DatedOptions` through to `dict.BuildDictionary`, and point its
   `go.mod` at this fork with a `replace` directive.
5. **Upstream PR** to Runik-3/builder once it's proven on the real wiki.

## Known limits

- Categories are today's, not the cutoff's. Harmless: text is still dated.
- Templates in old revisions are parsed by the same parser; infobox-heavy
  pages may parse worse. Check the "no usable first sentence" count.
- Preview chapters were published months before some releases (Rhythm of War
  from mid-2020); a 3-day buffer won't hide wiki edits based on them. Use
  `-buffer` or `-before` if that matters.
- `rdlimit=max` (500) redirects per page; more are ignored.
- Be polite to the wiki: keep `-rate` low and pass `-contact`.

## Working conventions

- Go standard library only (no dependencies in `go.mod`); keep it that way.
- Run `gofmt`, `go vet ./...`, `go test ./...` and `GOOS=js GOARCH=wasm go build -o /dev/null .`
  (the package also compiles to WASM for Runik's web use).
- Tests must not hit the network: extend `internal/fakewiki` instead.
- Several upstream files aren't gofmt-clean; don't reformat them in feature commits.
