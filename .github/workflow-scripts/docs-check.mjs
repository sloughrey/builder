#!/usr/bin/env node
/**
 * Fails when the code and the documentation disagree on things that can be checked
 * mechanically. Rules and paths come from `docsCheck` in `.claude/workflow.json`:
 *
 *   apiRoutes   every route in the controllers has a "### `METHOD /path`" heading in the doc, and vice versa
 *   envVars     every key of the env schema is in the example env file and the doc
 *   scripts     every package.json script is mentioned in the doc
 *   modules     every folder in a source dir is mentioned in the doc as "<folder>/"
 *   workspaces  every workspace is mentioned in the doc (and has a README.md)
 *   files       every file matching each entry's glob is named in that entry's doc
 *               (e.g. every page component in the architecture doc)
 *
 * A rule set to `false` is off. A rule whose code side doesn't exist (no controllers, no env
 * schema, no package.json, ...) or whose doc file is missing is skipped and reported as such.
 *
 *   node docs-check.mjs [--root <dir>] [--config <workflow.json>]
 */
import { existsSync, readdirSync, readFileSync, statSync } from 'node:fs';
import { join } from 'node:path';
import { DEFAULTS, loadConfig, mergeConfig, repoRoot } from './lib/config.mjs';
import { findFiles, matchesGlob } from './lib/glob.mjs';

const HTTP_METHODS = ['Get', 'Post', 'Put', 'Patch', 'Delete'];

const stripQuotes = (value) => value?.trim().replace(/^['"`]|['"`]$/g, '') ?? '';
const joinPath = (...parts) =>
  `/${parts
    .map((part) => stripQuotes(part).replace(/^\/+|\/+$/g, ''))
    .filter(Boolean)
    .join('/')}`;

/** Routes declared in one NestJS controller file, as "METHOD /path" (without a global prefix). */
export function extractRoutes(source) {
  const controller = /@Controller\(\s*([^)]*)\)/.exec(source);
  if (!controller) return [];
  const prefix = controller[1];
  const routes = [];
  const pattern = new RegExp(`@(${HTTP_METHODS.join('|')})\\(\\s*([^)]*)\\)`, 'g');
  for (const match of source.matchAll(pattern)) {
    routes.push(`${match[1].toUpperCase()} ${joinPath(prefix, match[2])}`);
  }
  return routes;
}

/** Routes documented as "### `METHOD /path`" headings. */
export function documentedRoutes(apiDoc) {
  return [...apiDoc.matchAll(/^###\s+`(GET|POST|PUT|PATCH|DELETE)\s+(\/[^`\s]*)`/gm)].map(
    (match) => `${match[1]} ${match[2].replace(/\/+$/, '') || '/'}`,
  );
}

/** Keys of `<exportName> = z.object({ ... })`. */
export function extractEnvVars(schemaSource, exportName = 'envSchema') {
  const start = schemaSource.indexOf(`${exportName} = z.object({`);
  if (start === -1) return [];
  const end = schemaSource.indexOf('\n});', start);
  const block = schemaSource.slice(start, end === -1 ? undefined : end);
  return [...block.matchAll(/^\s+([A-Z][A-Z0-9_]*)\s*:/gm)].map((match) => match[1]);
}

/** Whether a package.json script is mentioned in a doc. */
export function mentionsScript(doc, script) {
  const forms = [`npm run ${script}`, `\`${script}\``];
  if (['test', 'start'].includes(script)) forms.push(`npm ${script}`);
  return forms.some((form) => doc.includes(form));
}

const escapeRegExp = (text) => text.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');

/**
 * The name a doc must use for a file: its file name (`match: "name"`, e.g. `guides.ts`) or its
 * name without the last extension (`match: "stem"`, e.g. `SettingsView` for `SettingsView.vue`).
 */
export function fileLabel(path, match = 'stem') {
  const name = path.split('/').pop();
  return match === 'name' ? name : name.replace(/\.[^.]+$/, '');
}

/** Whether a doc names a file as a whole word (case-sensitive), so `Help` doesn't match `HelpLink`. */
export function mentionsFile(doc, label) {
  return new RegExp(`(?<![\\w.-])${escapeRegExp(label)}(?![\\w-])`).test(doc);
}

/* ---- Rules: pure functions from gathered facts to problems ---- */

export function checkApiRoutes({ routes, apiDoc, docPath }) {
  const problems = [];
  const documented = new Set(documentedRoutes(apiDoc));
  const actual = new Set(routes);
  for (const route of actual) {
    if (!documented.has(route)) problems.push(`${docPath}: missing endpoint \`${route}\``);
  }
  for (const route of documented) {
    if (!actual.has(route)) {
      problems.push(`${docPath}: documents \`${route}\`, which no controller defines`);
    }
  }
  return problems;
}

export function checkEnvVars({ envVars, envExample, examplePath, doc, docPath }) {
  const problems = [];
  for (const name of envVars) {
    if (!new RegExp(`^#?\\s*${name}=`, 'm').test(envExample)) {
      problems.push(`${examplePath}: missing ${name}`);
    }
    if (!doc.includes(`\`${name}\``)) {
      problems.push(`${docPath}: environment variable ${name} is not documented`);
    }
  }
  return problems;
}

export function checkScripts({ scripts, doc, docPath }) {
  return scripts
    .filter((script) => !mentionsScript(doc, script))
    .map((script) => `${docPath}: script "${script}" is not documented`);
}

export function checkModules({ modules, dir, doc, docPath }) {
  return modules
    .filter((module) => !doc.includes(`${module}/`))
    .map((module) => `${docPath}: folder ${dir}/${module}/ is not described`);
}

export function checkWorkspaces({ workspaces, doc, docPath, requireReadme }) {
  const problems = [];
  for (const { path, hasReadme } of workspaces) {
    if (!doc.includes(path)) problems.push(`${docPath}: workspace ${path} is not described`);
    if (requireReadme && !hasReadme) problems.push(`${path}/README.md: missing`);
  }
  return problems;
}

export function checkFiles({ files, doc, docPath, match }) {
  return files
    .filter((file) => !mentionsFile(doc, fileLabel(file, match)))
    .map((file) => `${docPath}: ${fileLabel(file, match)} (${file}) is not mentioned`);
}

/* ---- Gathering facts from a repository ---- */

const read = (root, path) =>
  path && existsSync(join(root, path)) ? readFileSync(join(root, path), 'utf8') : null;
const dirs = (path) =>
  existsSync(path)
    ? readdirSync(path).filter((name) => statSync(join(path, name)).isDirectory())
    : [];

/**
 * Runs every configured rule against the repository at `root`.
 * Returns [{ rule, problems, summary } | { rule, skipped }].
 */
export function runChecks(root, docsCheck) {
  const results = [];
  const skip = (rule, reason) => results.push({ rule, skipped: reason });
  const needDoc = (rule, path) => {
    const doc = read(root, path);
    if (doc === null) skip(rule, `${path} not found`);
    return doc;
  };

  const api = docsCheck.apiRoutes;
  if (!api) skip('apiRoutes', 'off');
  else {
    const files = findFiles(root, api.controllers);
    if (!files.length) skip('apiRoutes', `no files match ${api.controllers}`);
    else {
      const apiDoc = needDoc('apiRoutes', api.doc);
      if (apiDoc !== null) {
        const routes = files.flatMap((file) => extractRoutes(read(root, file)));
        results.push({
          rule: 'apiRoutes',
          problems: checkApiRoutes({ routes, apiDoc, docPath: api.doc }),
          summary: `${routes.length} routes`,
        });
      }
    }
  }

  const env = docsCheck.envVars;
  if (!env) skip('envVars', 'off');
  else {
    const schema = read(root, env.schema);
    if (schema === null) skip('envVars', `${env.schema} not found`);
    else {
      const doc = needDoc('envVars', env.doc);
      if (doc !== null) {
        const envVars = extractEnvVars(schema, env.exportName);
        results.push({
          rule: 'envVars',
          problems: checkEnvVars({
            envVars,
            envExample: read(root, env.example) ?? '',
            examplePath: env.example,
            doc,
            docPath: env.doc,
          }),
          summary: `${envVars.length} env vars`,
        });
      }
    }
  }

  const scr = docsCheck.scripts;
  if (!scr) skip('scripts', 'off');
  else {
    const pkg = read(root, scr.packageJson);
    if (pkg === null) skip('scripts', `${scr.packageJson} not found`);
    else {
      const doc = needDoc('scripts', scr.doc);
      if (doc !== null) {
        const scripts = Object.keys(JSON.parse(pkg).scripts ?? {});
        results.push({
          rule: 'scripts',
          problems: checkScripts({ scripts, doc, docPath: scr.doc }),
          summary: `${scripts.length} scripts`,
        });
      }
    }
  }

  const mod = docsCheck.modules;
  if (!mod || !mod.dir) skip('modules', 'off');
  else if (!existsSync(join(root, mod.dir))) skip('modules', `${mod.dir} not found`);
  else {
    const doc = needDoc('modules', mod.doc);
    if (doc !== null) {
      const modules = dirs(join(root, mod.dir));
      results.push({
        rule: 'modules',
        problems: checkModules({ modules, dir: mod.dir, doc, docPath: mod.doc }),
        summary: `${modules.length} folders in ${mod.dir}`,
      });
    }
  }

  const ws = docsCheck.workspaces;
  if (!ws) skip('workspaces', 'off');
  else {
    const workspaces = ws.groups.flatMap((group) =>
      dirs(join(root, group))
        .filter((name) => existsSync(join(root, group, name, 'package.json')))
        .map((name) => ({
          path: `${group}/${name}`,
          hasReadme: existsSync(join(root, group, name, 'README.md')),
        })),
    );
    if (!workspaces.length) skip('workspaces', `no workspaces in ${ws.groups.join(', ')}`);
    else {
      const doc = needDoc('workspaces', ws.doc);
      if (doc !== null) {
        results.push({
          rule: 'workspaces',
          problems: checkWorkspaces({
            workspaces,
            doc,
            docPath: ws.doc,
            requireReadme: ws.requireReadme,
          }),
          summary: `${workspaces.length} workspaces`,
        });
      }
    }
  }

  const entries = docsCheck.files || [];
  if (!entries.length) skip('files', 'off');
  else {
    const problems = [];
    let count = 0;
    let checked = 0;
    for (const { glob, doc: docPath, exclude = [], match = 'stem' } of entries) {
      const files = findFiles(root, glob).filter(
        (file) => !exclude.some((pattern) => matchesGlob(file, pattern)),
      );
      if (!files.length) continue;
      const doc = read(root, docPath);
      if (doc === null) {
        problems.push(`${docPath}: not found (needed to describe ${glob})`);
        continue;
      }
      checked += 1;
      count += files.length;
      problems.push(...checkFiles({ files, doc, docPath, match }));
    }
    if (!checked && !problems.length) skip('files', 'no files match any entry');
    else results.push({ rule: 'files', problems, summary: `${count} files` });
  }
  return results;
}

function main() {
  const argv = process.argv.slice(2);
  const rootFlag = argv.indexOf('--root');
  const configFlag = argv.indexOf('--config');
  const root = rootFlag === -1 ? repoRoot() : argv[rootFlag + 1];
  const config =
    configFlag === -1
      ? loadConfig(root)
      : mergeConfig(
          structuredClone(DEFAULTS),
          JSON.parse(readFileSync(argv[configFlag + 1], 'utf8')),
        );
  const results = runChecks(root, config.docsCheck);
  const problems = results.flatMap((result) => result.problems ?? []);
  const skipped = results.filter((result) => result.skipped && result.skipped !== 'off');

  if (problems.length) {
    console.error(
      `Documentation is out of date (${problems.length} problem${problems.length === 1 ? '' : 's'}):`,
    );
    for (const problem of problems) console.error(`  - ${problem}`);
    console.error(`\nSee ${config.docs.guide}, or run /structured:docs-sync to update the docs.`);
    process.exit(1);
  }
  const checked = results.filter((result) => result.summary).map((result) => result.summary);
  console.log(
    checked.length ? `Docs are in sync: ${checked.join(', ')}.` : 'Docs check: no rules apply.',
  );
  for (const { rule, skipped: reason } of skipped) console.log(`  (skipped ${rule}: ${reason})`);
}

if (process.argv[1]?.endsWith('docs-check.mjs')) {
  try {
    main();
  } catch (error) {
    console.error(`docs-check: ${error.message}`);
    process.exit(1);
  }
}
