# runik

Go CLI (fork of Runik-3/builder) that turns a MediaWiki wiki into an e-reader dictionary. This fork adds spoiler-free builds: definitions come from wiki revisions dated just before a chosen book's release. See `HANDOFF.md` for design and status.

## Stack and layout

Go (see `go.mod`), also compiled to WASM.

| Folder | Purpose |
| --- | --- |
| `main.go` | CLI entry (`generate`, `books`, flags) |
| `dict/` | Dictionary building, dated builds, output formatters |
| `wikiBot/` | MediaWiki API client, batch pages, dated revisions |
| `internal/` | `wikitext` parser, `fakewiki` test server, utils |
| `presets/` | Universe/book presets (`coppermind.json`) |
| `wasm/` | WASM registration |

Docs: `docs/workflow.md` (how we work) and `docs/documentation.md` (which doc covers what and when to update it).

## Commands

```bash
make test                      # go test ./...
go vet ./...
go build -o builder.exe .
GOOS=js GOARCH=wasm go build ./...
```

## Workflow

This project uses the `structured` workflow plugin; settings are in `.claude/workflow.json`.

1. Every change has an issue in the current milestone (`release.current` in `.claude/workflow.json`). Draft one with `/structured:new-issue`.
2. Branch off the current release branch (`release/vX.Y.Z`): `<type>/<issue#>-<slug>` with type `feature`, `fix`, `chore`, `docs`.
3. Conventional Commits (`feat(scope): …`) with `Refs #<issue>`, ending with the `attribution.commit` line from the config if set.
4. PR into the release branch using `.github/pull_request_template.md`: `Closes #n`, how it was tested, **Deployment notes** (`None` if nothing applies), docs checklist. Run `/structured:docs-sync` and `/structured:secrets-scan` before opening it; don't open it while the scan says BLOCK. Never commit secrets or real personal data: use environment variables and synthetic test data. If the commit hook blocks a commit, fix the change; ask the user before allowlisting a false positive.
5. The user approves merging. Then land it with `/structured:land-pr <pr>`. Exit code 3 means not ready (e.g. CI running): report it, don't poll.
6. The release PR (`release/vX.Y.Z` → `main`) uses `.github/PULL_REQUEST_TEMPLATE/release.md`; refresh it with `/structured:release-sync`.

Model routing comes from the plugin: its rules are added at session start (see `docs/workflow.md`). The main session plans and reviews; `Explore` (Haiku) locates, `structured:researcher` (Sonnet) explains, `structured:implementer` builds (Sonnet, or Opus for judgement work), `structured:reviewer` (Opus) reviews. Chores run on Haiku, docs on Sonnet.

### Needs Opus

<!-- Areas where implementation always goes to Opus (implementer with model "opus"), e.g. auth, billing rules, migrations. -->

## Conventions

Tests use the in-memory fake wiki (`internal/fakewiki`); don't hit real wikis in tests.

## Gotchas

The `.claude/workflow.json` `docsCheck` is `false` (Go project, no matching doc rules).
