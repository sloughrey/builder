# Documentation Guide

How this project's documentation is organised, when it must change, and how to write it. The `docs-writer` agent and `/structured:docs-sync` follow this guide, and the docs check (`node .github/workflow-scripts/docs-check.mjs`) enforces the parts that can be checked automatically.

This is a starter created by `/structured:workflow-init`. The project owns it: adapt the tables to the project.

## Where things are documented

| Document               | Audience        | Covers                                                               |
| ---------------------- | --------------- | -------------------------------------------------------------------- |
| `README.md`            | Everyone        | What the project is, quick start, links to the docs below            |
| `docs/architecture.md` | Developers      | How the system fits together: components, data model, key rules      |
| `docs/development.md`  | Developers      | Setup, scripts, environment variables, testing, troubleshooting      |
| `CLAUDE.md`            | Claude sessions | Short project brief: stack, commands, workflow, conventions, gotchas |

How we work (issues, branches, PRs, releases, model routing) is in `docs/workflow.md`; this guide is `docs/documentation.md`.

## What must be updated when

Update docs **in the same PR** as the change.

| When a PR…                                                   | Update                                                         |
| ------------------------------------------------------------ | -------------------------------------------------------------- |
| adds or changes a component, module or data model            | `docs/architecture.md`; Deployment notes if data migrates      |
| adds or changes an environment variable                      | `.env.example`, `docs/development.md`, Deployment notes        |
| adds or changes a script                                     | `docs/development.md`                                          |
| changes user-facing behaviour                                | `README.md` and any user guide                                 |
| changes how we work (branches, CI, release, tooling, agents) | `CLAUDE.md` (the workflow doc is managed by the plugin)        |
| hits a non-obvious problem that cost time                    | `docs/development.md` → Troubleshooting, `CLAUDE.md` → Gotchas |

### Checked automatically

The docs check runs the rules enabled under `docsCheck` in `.claude/workflow.json` (API routes, environment variables, package scripts, source folders, workspaces, and files such as pages and components that must be named in a doc). Rules whose code doesn't exist in this project are skipped. Everything else is reviewed by hand or with `/structured:docs-sync`.

## Writing style

- Write for the reader of that document.
- Short sections, tables for reference material, numbered steps for procedures, code blocks for commands.
- Describe what is true **now**; don't narrate history. Note the issue that introduced a section in its heading, e.g. `## Authentication — #5`.
- Prefer one source of truth: link to another doc instead of repeating it.
- Keep examples real: commands that actually run, payloads that match the code.
