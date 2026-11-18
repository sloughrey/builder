# Development Workflow

How work is planned, built, reviewed and released. This file is managed by the `structured` Claude Code plugin (`/structured:workflow-init` keeps it in step); put project-specific details in `CLAUDE.md` or the development docs. Project settings live in `.claude/workflow.json`.

## Branches

- **`main`** — released code only. Each merge here is a tagged release.
- **`release/vX.Y.Z`** — integration branch for the version in progress (`release.current` in `.claude/workflow.json`).
- **`<type>/<issue#>-<slug>`** — work for one issue. Types: `feature`, `fix`, `chore`, `docs`.

Work branches are always created from the **current** release branch:

```bash
git checkout release/vX.Y.Z
git pull
git checkout -b feature/5-short-slug
```

## Issues

- Every piece of work has a GitHub issue before code is written. `/structured:new-issue` drafts one from a short description (or files a supplied plan as-is).
- Issues belong to a **milestone** named after the version (e.g. `v0.1.0`) and carry labels: `type:feature`, `type:bug`, `type:chore`, `type:test`, `area:docs`, `area:infra`, `area:dict`, `area:wiki`, `area:wasm`.
- Issues include a description, acceptance criteria, a task checklist and any dependencies (`Depends on #n`), following the templates in `.github/ISSUE_TEMPLATE/`.

## Commits

Use [Conventional Commits](https://www.conventionalcommits.org/):

```
<type>(<scope>): <summary>

<body>

Refs #<issue>
```

- Types: `feat`, `fix`, `docs`, `chore`, `test`, `refactor`, `ci`, `build`
- Example: `feat(api): add invite-code join endpoint` + `Refs #6`
- If `attribution.commit` is set in `.claude/workflow.json`, commits made by Claude end with that line.

## Pull requests

- **One PR per issue**, from the work branch into the release branch.
- Fill in the PR template: summary, `Closes #<issue>`, how it was tested, **deployment notes**, screenshots for UI changes, and the docs checklist.
  - The first paragraph of **Summary** becomes this PR's one-line description in the release notes — keep it short and plain.
  - **Deployment notes** record anything needed to ship the change: environment variables, database changes or migrations, infrastructure, manual steps, breaking changes. Write `None` if nothing applies. They are collected into the release PR automatically.
- CI must pass before merging.
- Every PR updates the documentation it affects, in the same PR. [docs/documentation.md](docs/documentation.md) says which document covers what.
  - Before opening the PR, run **`/structured:docs-sync`** (or ask for the `docs-writer` agent).
  - The **docs check** (`node .github/workflow-scripts/docs-check.mjs`, run in CI) fails when code and docs disagree on what it can check mechanically. Its rules are configured under `docsCheck` in `.claude/workflow.json`.
- No PR adds secrets or real personal data. Three checks, each catching what the earlier one couldn't:
  - **On commit**: when Claude runs `git commit`, the plugin's hook scans the uncommitted changes. Likely secrets (keys, tokens, private keys, `.env` files, card numbers, SSNs) **block** the commit; possible personal data (emails, phone numbers, IBANs) or weaker matches **ask** you first.
  - **Before opening the PR**: run **`/structured:secrets-scan`** (or ask for the `secrets-scanner` agent). It runs the scanner over the branch and every commit on it, judges each hit in context, and reads the diff for what patterns miss: tokens in URLs, credentials in CI or Docker files, realistic personal records in fixtures, logging that prints secrets or personal fields.
  - **In CI**: `node .github/workflow-scripts/secrets-scan.mjs --base origin/<base> --history` fails the PR on any high or medium finding.
  - Use synthetic test data (`jane@example.com`, `555-0100`, payment providers' test cards). Silence a false positive with a `secrets-scan: allow` comment on the line or a regex in `secretsScan.allow` in `.claude/workflow.json`.
  - **A secret that was pushed is compromised**, even if a later commit removes it: rotate it first, then remove it. One that is only in local commits can be removed by rewriting the branch before pushing.
- The repository owner approves merging. Then **land** it with **`/structured:land-pr <number>`**.

  The landing script refuses unless the PR is open, not a draft, mergeable and every CI check passed (exit code 3 means "not ready yet", e.g. CI still running). Then it:
  1. squash-merges as `<title> (#<pr>)` and deletes the remote branch
  2. closes each issue referenced with `Closes/Fixes/Resolves #n` — GitHub only does this itself for merges into the default branch
  3. checks out the base branch, fast-forwards it and deletes the local work branch
  4. refreshes the open release PR's generated notes

  **Stacked PRs** (a PR whose base is another PR's branch) are first pointed at the landed PR's base, because GitHub closes a PR whose base branch is deleted. The script then prints the `git rebase --onto …` and `git push --force-with-lease` commands that replay only the stacked branch's own commits; run them before landing the stacked PR.

## Releases

Each version has a **release PR** from `release/vX.Y.Z` into `main`. Open it early (as a draft) so it always shows what is in the release.

1. Open it with the release template:
   ```bash
   gh pr create --draft --base main --head release/vX.Y.Z --template release.md --title "Release vX.Y.Z"
   ```
2. Run **`/structured:release-sync`** to fill the generated sections; landing a PR refreshes them automatically. Only the text between the `<!-- release-notes:… -->` markers is rewritten:
   - **Included changes** — one row per merged PR, oldest first: linked issue(s), linked PR and the first paragraph of its Summary.
   - **Collected from pull requests** — each PR's Deployment notes.
3. Consolidate the collected notes into the release PR's **Deployment notes** sections by hand, and work through its **Release checklist**.
4. When every issue in the milestone is closed, merge the release PR (merge commit, not squash), tag `vX.Y.Z`, create a GitHub release, and close the milestone.
5. Set `release.current` in `.claude/workflow.json` to the next version and run `/structured:workflow-init` to create its milestone and branch.

## Working with Claude Code

- Claude creates the branch, implements the issue, updates docs, and opens the PR. The repository owner reviews and approves merging.
- `CLAUDE.md` gives every session the project's stack, commands, conventions and gotchas.

### Model routing

The main session (whatever model you pick) plans, designs, splits work into tasks and reads every diff. It hands the rest to agents pinned to cheaper models. The plugin adds these routing rules to every session at start (`rules/model-routing.md`, through a `SessionStart` hook); set `"modelRouting": false` in `.claude/workflow.json` to turn that off.

To check the routing is working, watch for the `[model-routing] <phase> -> <model> (<agent>)` lines. A `PreToolUse` hook (`scripts/announce-model.mjs`) prints one whenever a subagent or skill starts, using the `model` on the Agent call or the agent's pinned model. The main session announces its own inline phases the same way. The same opt-out turns them off.

| Agent                    | Runs on         | Use it to                                                                                                       |
| ------------------------ | --------------- | --------------------------------------------------------------------------------------------------------------- |
| `Explore`                | Haiku           | Locate files, symbols, callers and conventions. A managed copy in `.claude/agents/` replaces the built-in one   |
| `structured:researcher`  | Sonnet          | Understand: trace a flow across modules, explain a subsystem, summarise long logs or docs. Read-only            |
| `structured:implementer` | Sonnet, or Opus | Build one well-specified task from a brief (files, pattern to copy, acceptance criteria, checks). Never commits |
| `structured:reviewer`    | Opus            | Review a diff against its brief and the project's conventions. Reports only                                     |

The implementer goes to Opus (pass `model: "opus"`) for work that needs judgement: auth and security, money or core business rules, migrations, changes across packages, and the areas the project's `CLAUDE.md` lists under **Needs Opus**. Small edits (under about 30 lines in files already open) are done inline. A failed or doubtful result moves up one tier rather than being retried.

Chores and docs don't need the most capable model either. The plugin's commands and agents pin cheaper models:

| Use                                           | Runs on | What it does                                                                                                                  |
| --------------------------------------------- | ------- | ----------------------------------------------------------------------------------------------------------------------------- |
| `/structured:land-pr <n>`                     | Haiku   | Runs the landing script and reports in two lines; stops if CI isn't green                                                     |
| `/structured:release-sync [branch]`           | Haiku   | Refreshes the release PR's generated sections                                                                                 |
| `repo-chores` agent                           | Haiku   | Mechanical GitHub/git work with all content supplied: open a PR from a given body, edit PR/issue text, labels, branch cleanup |
| `/structured:docs-sync [base] [notes]`        | Sonnet  | Updates every doc affected by the current branch, then runs the docs check                                                    |
| `docs-writer` agent                           | Sonnet  | Same job, for delegation. Edits docs only                                                                                     |
| `/structured:secrets-scan [scope]`            | Sonnet  | Checks the branch (or staged/uncommitted work) for secrets and personal data; reports BLOCK / REVIEW / CLEAR                  |
| `secrets-scanner` agent                       | Sonnet  | Same job, for delegation. Reports only, never edits                                                                           |
| `/structured:new-issue <description or plan>` | Sonnet  | Files a supplied plan as-is, or drafts a full issue (criteria, tasks, labels, milestone)                                      |
| `/structured:workflow-init`                   | Sonnet  | Sets the project up, or shows what changed in the plugin's templates since the last run                                       |

Issues are never drafted on Haiku unless a complete plan is supplied.
