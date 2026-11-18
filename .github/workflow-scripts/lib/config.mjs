/**
 * Loads a project's workflow settings from `.claude/workflow.json` and fills in defaults.
 * Every script in this plugin reads its project-specific values from here.
 *
 * Merge rules: objects merge key by key; arrays and scalars in the project file replace the
 * default. Set a docs-check rule to `false` to turn it off.
 */
import { execFileSync } from 'node:child_process';
import { existsSync, readFileSync } from 'node:fs';
import { join } from 'node:path';

export const CONFIG_PATH = '.claude/workflow.json';

export const DEFAULTS = Object.freeze({
  release: {
    /** Long-lived branch that receives releases (the release PR's base). */
    baseBranch: 'master',
    /** Release branches are `<branchPrefix><version>`, e.g. release/v0.1.0. */
    branchPrefix: 'release/',
    /** Version in progress, e.g. "v0.1.0". Also the milestone name. Detected from branches when empty. */
    current: '',
  },
  branchTypes: ['feature', 'fix', 'chore', 'docs'],
  labels: [
    { name: 'type:feature', color: '1d76db', description: 'New functionality' },
    { name: 'type:bug', color: 'd73a4a', description: "Something isn't working" },
    { name: 'type:chore', color: 'c5def5', description: 'Tooling, config, CI, dependencies' },
    { name: 'type:test', color: 'bfd4f2', description: 'Tests only' },
    { name: 'area:docs', color: '0e8a16', description: 'Documentation' },
    { name: 'area:infra', color: 'fbca04', description: 'CI, build, tooling, deployment' },
  ],
  docs: {
    workflow: 'docs/workflow.md',
    guide: 'docs/documentation.md',
    /** Files the docs tools may edit (globs). Everything else is code and off limits. */
    editable: [
      'README.md',
      'CLAUDE.md',
      'CONTRIBUTING.md',
      '.env.example',
      'docs/**',
      '**/README.md',
    ],
  },
  docsCheck: {
    /** NestJS-style controllers vs "### `METHOD /path`" headings. */
    apiRoutes: { controllers: '**/*.controller.ts', doc: 'docs/api.md' },
    /** Keys of `<exportName> = z.object({ ... })` vs the example env file and a doc. */
    envVars: {
      schema: 'src/config/env.schema.ts',
      exportName: 'envSchema',
      example: '.env.example',
      doc: 'docs/development.md',
    },
    /** Root package.json scripts vs a doc. */
    scripts: { packageJson: 'package.json', doc: 'docs/development.md' },
    /** Every folder in `dir` is mentioned as "<folder>/" in a doc. Off unless `dir` is set. */
    modules: { dir: '', doc: 'docs/architecture.md' },
    /** Workspaces (`<group>/<name>/package.json`) are mentioned in a doc and have a README. */
    workspaces: { groups: ['apps', 'packages'], doc: 'docs/architecture.md', requireReadme: true },
    /**
     * Every file matching an entry's `glob` (minus `exclude` globs) is named in its `doc`, by
     * `match: "stem"` (file name without extension, the default) or `"name"`. Off when empty.
     * e.g. [{ glob: 'apps/web/src/views/*.vue', doc: 'docs/architecture.md' }]
     */
    files: [],
  },
  /** Secrets and personal-data scan (secrets-scan.mjs). Set the whole key to `false` to turn it off. */
  secretsScan: {
    /** Check `git commit` calls Claude makes: block high findings, ask about medium ones. */
    commitHook: true,
    /** Paths never scanned (globs). */
    ignorePaths: [
      '**/package-lock.json',
      '**/yarn.lock',
      '**/pnpm-lock.yaml',
      '**/*.min.js',
      '**/*.min.css',
      '**/*.map',
      '.claude/workflow.json',
    ],
    /** Regular expressions; a finding whose value matches one is dropped. */
    allow: [],
    /** Rule ids to turn off, e.g. "ipv4-public". */
    disable: [],
  },
  /** Lines appended to commit messages / PR bodies. Empty: use Claude Code's own attribution. */
  attribution: { commit: '', pr: '' },
});

const isObject = (value) => value !== null && typeof value === 'object' && !Array.isArray(value);

/** Deep-merges `override` onto `base` (objects merge; arrays, scalars and `false` replace). */
export function mergeConfig(base, override) {
  if (!isObject(base) || !isObject(override)) return override === undefined ? base : override;
  const result = { ...base };
  for (const [key, value] of Object.entries(override)) {
    if (key === '$schema') continue;
    result[key] = key in base ? mergeConfig(base[key], value) : value;
  }
  return result;
}

/** The repository root (git top level), or the current directory outside a checkout. */
export function repoRoot(cwd = process.cwd()) {
  try {
    return execFileSync('git', ['rev-parse', '--show-toplevel'], {
      cwd,
      encoding: 'utf8',
      stdio: ['ignore', 'pipe', 'ignore'],
    }).trim();
  } catch {
    return cwd;
  }
}

/** Reads `.claude/workflow.json` under `root` (if any) merged onto the defaults. */
export function loadConfig(root = repoRoot()) {
  const file = join(root, CONFIG_PATH);
  if (!existsSync(file)) return structuredClone(DEFAULTS);
  let parsed;
  try {
    parsed = JSON.parse(readFileSync(file, 'utf8'));
  } catch (error) {
    throw new Error(`${CONFIG_PATH} is not valid JSON: ${error.message}`);
  }
  return mergeConfig(structuredClone(DEFAULTS), parsed);
}

/** The milestone / version for a release branch ("release/v0.1.0" → "v0.1.0"), or null. */
export function versionOf(branch, config = DEFAULTS) {
  const prefix = config.release.branchPrefix;
  return branch?.startsWith(prefix) ? branch.slice(prefix.length) : null;
}

export const isReleaseBranch = (branch, config = DEFAULTS) => versionOf(branch, config) !== null;
