#!/usr/bin/env node
/**
 * Finds likely secrets (API keys, tokens, private keys, passwords) and personal data (emails,
 * phone numbers, card numbers, national IDs, IBANs) in the lines a change ADDS. Removed and
 * unchanged lines are ignored. Matched values are never printed in full.
 *
 *   node secrets-scan.mjs                     branch changes: the release branch (or --base) to the working tree, plus untracked files
 *   node secrets-scan.mjs --base <ref>        the same against <ref>, e.g. origin/master in CI
 *   node secrets-scan.mjs --base <ref> --history   also every commit since <ref>: a secret added and later removed is still in history
 *   node secrets-scan.mjs --staged            only what `git commit` would commit right now
 *   node secrets-scan.mjs --uncommitted       everything not committed yet (tracked changes and untracked files)
 *   node secrets-scan.mjs --json              machine-readable output
 *   node secrets-scan.mjs --hook              Claude Code PreToolUse hook: reads the tool call on stdin and
 *                                             blocks (high) or asks about (medium) a `git commit` that adds findings
 *
 * Severity: high (almost certainly a secret or sensitive personal data), medium (likely; needs a
 * look), low (reported, never fails). Exit codes: 0 nothing high or medium, 1 findings, 2 error.
 *
 * Suppressing a false positive: put `secrets-scan: allow` in a comment on that line, or add a
 * regular expression matching the value to `secretsScan.allow` in `.claude/workflow.json`.
 * `secretsScan.ignorePaths` (globs) and `secretsScan.disable` (rule ids) are also available.
 */
import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { existsSync, readFileSync, statSync } from 'node:fs';
import { basename, join } from 'node:path';
import { DEFAULTS, loadConfig, mergeConfig, repoRoot } from './lib/config.mjs';
import { matchesGlob } from './lib/glob.mjs';

const EMPTY_TREE = '4b825dc642cb6eb9a060e54bf8d69288fbee4904';
const MAX_FILE_BYTES = 2 * 1024 * 1024;
const INLINE_ALLOW = /secrets-scan:\s*allow|pragma:\s*allowlist secret/i;
export const SEVERITIES = ['high', 'medium', 'low'];

/* ---- Value checks ---- */

const PLACEHOLDER =
  /example|placeholder|change[_-]?me|your[_-]|<[^>]*>|\$\{|\{\{|%[sd(]|x{4,}|\*{3,}|\.{3}|dummy|fake|sample|redacted|replace|todo|not[_-]?a[_-]?real|^test|^(?:password|passwd|secret|token|pass|pwd|user)$/i;

/** Obvious stand-ins: "changeme", "${DB_PASSWORD}", "xxxx", "aaaaaaaa", ... */
export const isPlaceholder = (value) => PLACEHOLDER.test(value) || /^(.)\1+$/.test(value);

/** Luhn checksum, used by card numbers. */
export function luhn(digits) {
  let sum = 0;
  for (let i = 0; i < digits.length; i += 1) {
    let digit = Number(digits[digits.length - 1 - i]);
    if (i % 2 === 1) {
      digit *= 2;
      if (digit > 9) digit -= 9;
    }
    sum += digit;
  }
  return sum % 10 === 0;
}

/** IBAN mod-97 checksum. */
export function ibanValid(iban) {
  const compact = iban.replace(/ /g, '');
  if (compact.length < 15 || compact.length > 34) return false;
  let remainder = 0;
  for (const char of compact.slice(4) + compact.slice(0, 4)) {
    const value = /\d/.test(char) ? char : String(char.charCodeAt(0) - 55);
    for (const digit of value) remainder = (remainder * 10 + Number(digit)) % 97;
  }
  return remainder === 1;
}

/** Card numbers payment providers publish for testing. Committing these is fine. */
const TEST_CARDS = new Set([
  '4242424242424242',
  '4111111111111111',
  '4012888888881881',
  '4000056655665556',
  '5555555555554444',
  '5200828282828210',
  '5105105105105100',
  '2223003122003222',
  '378282246310005',
  '371449635398431',
  '6011111111111117',
  '6011000990139424',
  '3056930009020004',
  '36227206271667',
  '3566002020360505',
]);

function isRealCard(raw) {
  const digits = raw.replace(/[ -]/g, '');
  if (digits.length < 13 || digits.length > 19) return false;
  if (!/^(?:4|5[1-5]|2[2-7]|3[47]|6(?:011|5))/.test(digits)) return false;
  if (TEST_CARDS.has(digits) || digits.startsWith('400000000000') || /^(\d)\1+$/.test(digits)) {
    return false;
  }
  return luhn(digits);
}

const FILE_EXTENSION_TLD =
  /\.(?:png|jpe?g|gif|svg|webp|avif|ico|js|mjs|cjs|ts|tsx|jsx|css|scss|json|md|html?|xml|ya?ml|py|rb|go|rs|java|kt|php|txt)$/i;
const SAFE_EMAIL_DOMAIN =
  /(?:^|\.)(?:example\.(?:com|org|net)|example|test|invalid|localhost|local)$|noreply|no-reply/i;

function isRealEmail(value, context) {
  const [local, domain] = value.split('@');
  if (!domain || FILE_EXTENSION_TLD.test(domain) || SAFE_EMAIL_DOMAIN.test(domain)) return false;
  if (/^(?:git|no-?reply|noreply|user|name|someone|test|admin|info|support)$/i.test(local)) {
    return false;
  }
  return !context.ownEmails?.has(value.toLowerCase());
}

function isRealPhone(value) {
  const digits = value.replace(/\D/g, '');
  if (digits.length < 9 || digits.length > 15) return false;
  if (/^(\d)\1+$/.test(digits) || /1234567890|0123456789/.test(digits)) return false;
  return !/55501\d\d$/.test(digits); // fictional 555-01xx numbers
}

function isPublicIp(value) {
  const [a, b, c] = value.split('.').map(Number);
  if (a === 10 || a === 127 || a === 0 || a >= 224) return false;
  if ((a === 172 && b >= 16 && b <= 31) || (a === 192 && b === 168) || (a === 169 && b === 254)) {
    return false;
  }
  if (a === 100 && b >= 64 && b <= 127) return false;
  if (
    (a === 192 && b === 0 && c === 2) ||
    (a === 198 && b === 51 && c === 100) ||
    (a === 203 && b === 0 && c === 113)
  ) {
    return false; // documentation ranges
  }
  return true;
}

/** A generic "password = '...'" value: not a placeholder and not plain lowercase prose. */
const looksSecret = (value) =>
  !isPlaceholder(value) && (value.length >= 16 || /[A-Z0-9]/.test(value) || /[^\w-]/.test(value));

const SECRET_NAME =
  '[A-Za-z0-9_.-]*?(?:password|passwd|pwd|secret|api[_-]?key|apikey|access[_-]?key|access[_-]?token|auth[_-]?token|client[_-]?secret|private[_-]?key|token)';
const CONFIG_FILE =
  /(?:^|\/)\.env[^/]*$|\.(?:ini|cfg|conf|properties|ya?ml|toml)$|(?:^|\/)\.npmrc$/i;

/* ---- Rules, most specific first: a later rule never reports a span an earlier one already did ---- */

export const RULES = [
  // Secrets
  {
    id: 'private-key',
    kind: 'secret',
    severity: 'high',
    label: 'Private key',
    pattern: /-----BEGIN (?:[A-Z0-9]+ )*PRIVATE KEY(?: BLOCK)?-----/g,
  },
  {
    id: 'aws-access-key',
    kind: 'secret',
    severity: 'high',
    label: 'AWS access key ID',
    pattern: /\b(?:AKIA|ASIA|ABIA|ACCA)[0-9A-Z]{16}\b/g,
    validate: (value) => !/EXAMPLE$/.test(value),
  },
  {
    id: 'aws-secret-key',
    kind: 'secret',
    severity: 'high',
    label: 'AWS secret access key',
    pattern:
      /aws.{0,25}?(?:secret|private).{0,25}?[\s'"=:]([A-Za-z0-9/+=]{40})(?![A-Za-z0-9/+=])/gi,
    group: 1,
    validate: (value) => !isPlaceholder(value),
  },
  {
    id: 'github-token',
    kind: 'secret',
    severity: 'high',
    label: 'GitHub token',
    pattern: /\b(?:gh[pousr]_[A-Za-z0-9]{36,}|github_pat_[A-Za-z0-9_]{60,})\b/g,
  },
  {
    id: 'anthropic-key',
    kind: 'secret',
    severity: 'high',
    label: 'Anthropic API key',
    pattern: /\bsk-ant-[A-Za-z0-9_-]{20,}/g,
  },
  {
    id: 'openai-key',
    kind: 'secret',
    severity: 'high',
    label: 'OpenAI API key',
    pattern: /\bsk-(?!ant-)(?:proj-|svcacct-|admin-)?[A-Za-z0-9_-]{32,}/g,
    validate: (value) => /\d/.test(value) && /[A-Za-z]/.test(value.slice(3)),
  },
  {
    id: 'stripe-live-key',
    kind: 'secret',
    severity: 'high',
    label: 'Stripe live key',
    pattern: /\b(?:sk|rk)_live_[A-Za-z0-9]{16,}/g,
  },
  {
    id: 'stripe-test-key',
    kind: 'secret',
    severity: 'medium',
    label: 'Stripe test secret key',
    pattern: /\b(?:sk|rk)_test_[A-Za-z0-9]{16,}/g,
  },
  {
    id: 'slack-token',
    kind: 'secret',
    severity: 'high',
    label: 'Slack token',
    pattern: /\bxox[abposr]-[A-Za-z0-9-]{10,}/g,
  },
  {
    id: 'slack-webhook',
    kind: 'secret',
    severity: 'high',
    label: 'Slack webhook URL',
    pattern: /https:\/\/hooks\.slack\.com\/services\/[A-Za-z0-9_/]{20,}/g,
  },
  {
    id: 'npm-token',
    kind: 'secret',
    severity: 'high',
    label: 'npm token',
    pattern: /\bnpm_[A-Za-z0-9]{36}\b/g,
  },
  {
    id: 'sendgrid-key',
    kind: 'secret',
    severity: 'high',
    label: 'SendGrid API key',
    pattern: /\bSG\.[A-Za-z0-9_-]{22}\.[A-Za-z0-9_-]{43}\b/g,
  },
  {
    id: 'azure-storage-key',
    kind: 'secret',
    severity: 'high',
    label: 'Azure storage account key',
    pattern: /AccountKey=([A-Za-z0-9+/=]{60,})/g,
    group: 1,
  },
  {
    id: 'google-api-key',
    kind: 'secret',
    severity: 'medium',
    label: 'Google API key',
    pattern: /\bAIza[0-9A-Za-z_-]{35}(?![0-9A-Za-z_-])/g,
  },
  {
    id: 'url-credentials',
    kind: 'secret',
    severity: 'high',
    label: 'Password in a URL',
    pattern: /\b[a-z][a-z0-9+.-]{1,20}:\/\/[^\s:/?#@'"`]+:([^\s/?#@'"`]+)@[^\s'"`]+/gi,
    group: 1,
    validate: (value) => !isPlaceholder(value) && !value.startsWith('$'),
  },
  {
    id: 'npmrc-auth',
    kind: 'secret',
    severity: 'high',
    label: 'npm registry auth token',
    pattern: /_auth(?:Token)?\s*=\s*([^\s$'"]{8,})/g,
    group: 1,
    validate: (value) => !isPlaceholder(value),
  },
  {
    id: 'jwt',
    kind: 'secret',
    severity: 'medium',
    label: 'JSON Web Token',
    pattern: /\beyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}/g,
  },
  {
    id: 'secret-assignment',
    kind: 'secret',
    severity: 'medium',
    label: 'Hard-coded password or secret',
    pattern: new RegExp(
      `(?:^|[^\\w])${SECRET_NAME}["']?\\s*[:=]\\s*["'\`]([^"'\`\\s]{8,})["'\`]`,
      'gi',
    ),
    group: 1,
    validate: looksSecret,
  },
  {
    id: 'config-secret',
    kind: 'secret',
    severity: 'medium',
    label: 'Secret in a config file',
    pattern: new RegExp(`^\\s*(?:export\\s+)?${SECRET_NAME}\\s*[:=]\\s*([^\\s'"#]{8,})\\s*$`, 'gi'),
    group: 1,
    files: CONFIG_FILE,
    validate: looksSecret,
  },
  // Personal data
  {
    id: 'ssn',
    kind: 'pii',
    severity: 'high',
    label: 'US Social Security number',
    pattern: /(?<![\d-])(?!000|666|9\d\d)\d{3}-(?!00)\d{2}-(?!0000)\d{4}(?![\d-])/g,
    validate: (value) => !['123-45-6789', '078-05-1120'].includes(value),
  },
  {
    id: 'credit-card',
    kind: 'pii',
    severity: 'high',
    label: 'Payment card number',
    pattern: /(?<![\d-])(?:\d[ -]?){12,18}\d(?![\d-])/g,
    validate: isRealCard,
  },
  {
    id: 'iban',
    kind: 'pii',
    severity: 'medium',
    label: 'Bank account (IBAN)',
    pattern: /\b[A-Z]{2}\d{2}(?: ?[A-Z0-9]{4}){2,7}(?: ?[A-Z0-9]{1,4})?\b/g,
    validate: (value) =>
      ibanValid(value) &&
      !['GB82WEST12345698765432', 'DE89370400440532013000'].includes(value.replace(/ /g, '')),
  },
  {
    id: 'email',
    kind: 'pii',
    severity: 'medium',
    label: 'Email address',
    pattern: /\b[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}\b/g,
    validate: isRealEmail,
  },
  {
    id: 'phone',
    kind: 'pii',
    severity: 'medium',
    label: 'Phone number',
    pattern:
      /(?<![\w.+-])(?:\+\d{1,3}(?:[ .-]?\(?\d{1,5}\)?){2,5}|\(?\d{3}\)?[ .-]\d{3}[ .-]\d{4})(?![\w.-])/g,
    validate: isRealPhone,
  },
  {
    id: 'ipv4-public',
    kind: 'pii',
    severity: 'low',
    label: 'Public IP address',
    pattern:
      /(?<![\d.])(?:(?:25[0-5]|2[0-4]\d|1?\d?\d)\.){3}(?:25[0-5]|2[0-4]\d|1?\d?\d)(?![\d.])/g,
    validate: isPublicIp,
  },
].map((rule) => ({ ...rule, pattern: new RegExp(rule.pattern.source, `${rule.pattern.flags}d`) }));

/** Files that shouldn't be committed whatever they contain. Checked on the path alone. */
export const FILE_RULES = [
  {
    id: 'env-file',
    kind: 'secret',
    severity: 'high',
    label: 'Environment file',
    test: (name) =>
      /^\.env(?:\..+)?$/.test(name) &&
      !/\.(?:example|sample|template|dist|defaults?|schema)$/i.test(name),
  },
  {
    id: 'key-file',
    kind: 'secret',
    severity: 'high',
    label: 'Private key or keystore file',
    test: (name) =>
      /\.(?:pem|key|p12|pfx|jks|keystore|ppk|kdbx)$/i.test(name) ||
      /^id_(?:rsa|dsa|ecdsa|ed25519)$/.test(name) ||
      name === '.htpasswd',
  },
  {
    id: 'data-dump',
    kind: 'pii',
    severity: 'medium',
    label: 'Database file or data dump',
    test: (name) => /\.(?:sqlite3?|db|dump|bak|sql\.gz)$/i.test(name),
  },
];

/** A preview that identifies the value without revealing it. */
export function redact(value, ruleId) {
  if (ruleId === 'private-key') return value;
  if (ruleId === 'email') {
    const [local, domain] = value.split('@');
    return `${local[0]}…@${domain}`;
  }
  if (['credit-card', 'ssn', 'phone', 'iban'].includes(ruleId)) {
    return `…${value.replace(/\D/g, '').slice(-4)}`;
  }
  if (ruleId === 'ipv4-public') return value.replace(/\.\d+\.\d+$/, '.x.x');
  if (value.length <= 10) return `${value[0]}…`;
  return `${value.slice(0, 4)}…${value.slice(-2)}`;
}

const fingerprint = (value) => createHash('sha256').update(value).digest('hex').slice(0, 12);

/* ---- Scanning ---- */

/**
 * Scans added lines ({ file, line, text, commit? }) and file paths.
 * Options: disable (rule ids), allow (RegExps tested against the value), ownEmails (lower-cased).
 */
export function scan({ lines = [], files = [] }, options = {}) {
  const disabled = new Set(options.disable ?? []);
  const allow = options.allow ?? [];
  const context = { ownEmails: options.ownEmails ?? new Set() };
  const findings = [];
  const add = (rule, entry, value) => {
    if (allow.some((re) => re.test(value))) return;
    findings.push({
      severity: rule.severity,
      kind: rule.kind,
      rule: rule.id,
      label: rule.label,
      file: entry.file,
      line: entry.line ?? 0,
      ...(entry.commit ? { commit: entry.commit } : {}),
      preview: value === entry.file ? '' : redact(value, rule.id),
      fingerprint: fingerprint(value),
    });
  };

  for (const entry of files) {
    const name = basename(entry.file);
    for (const rule of FILE_RULES) {
      if (!disabled.has(rule.id) && rule.test(name)) add(rule, entry, entry.file);
    }
  }

  for (const entry of lines) {
    if (INLINE_ALLOW.test(entry.text)) continue;
    const taken = [];
    for (const rule of RULES) {
      if (disabled.has(rule.id) || (rule.files && !rule.files.test(entry.file))) continue;
      for (const match of entry.text.matchAll(rule.pattern)) {
        const group = rule.group ?? 0;
        const value = match[group];
        if (!value) continue;
        const [start, end] = match.indices[group];
        if (taken.some(([s, e]) => start < e && end > s)) continue;
        if (rule.validate && !rule.validate(value, context)) continue;
        taken.push([start, end]);
        add(rule, entry, value);
      }
    }
  }

  return findings.sort(
    (a, b) =>
      SEVERITIES.indexOf(a.severity) - SEVERITIES.indexOf(b.severity) ||
      a.file.localeCompare(b.file) ||
      a.line - b.line,
  );
}

/**
 * Added lines and touched files from `git diff -U0` / `git log -p -U0` output. Hunk line counts
 * are tracked, so an added line that itself starts with "+++" or "diff --git" isn't misread.
 * `git log` output must use `--format=commit:%H` so each line can be tied to its commit.
 */
export function parseDiff(diff) {
  const lines = [];
  const files = [];
  let file = null;
  let commit;
  let next = 0;
  let removed = 0;
  let added = 0;
  for (const rawLine of diff.split('\n')) {
    const raw = rawLine.replace(/\r$/, '');
    if (added > 0 || removed > 0) {
      if (raw.startsWith('+') && added > 0) {
        if (file) lines.push({ file, line: next, text: raw.slice(1), ...(commit && { commit }) });
        next += 1;
        added -= 1;
        continue;
      }
      if (raw.startsWith('-') && removed > 0) {
        removed -= 1;
        continue;
      }
      if (raw.startsWith('\\')) continue;
      added = 0;
      removed = 0;
    }
    const commitLine = /^commit:([0-9a-f]{7,40})$/.exec(raw);
    if (commitLine) {
      commit = commitLine[1];
      file = null;
      continue;
    }
    if (raw.startsWith('diff --git ')) {
      const at = raw.lastIndexOf(' b/');
      file = at === -1 ? null : raw.slice(at + 3);
      if (file) files.push({ file, ...(commit && { commit }) });
      continue;
    }
    if (raw.startsWith('+++ ')) {
      const path = raw.slice(4);
      file = path === '/dev/null' ? null : path.replace(/^b\//, '');
      continue;
    }
    const hunk = /^@@ -\d+(?:,(\d+))? \+(\d+)(?:,(\d+))? @@/.exec(raw);
    if (hunk) {
      removed = hunk[1] === undefined ? 1 : Number(hunk[1]);
      next = Number(hunk[2]);
      added = hunk[3] === undefined ? 1 : Number(hunk[3]);
    }
  }
  return { lines, files };
}

/* ---- Git ---- */

function git(root, args) {
  return execFileSync('git', ['-c', 'core.quotepath=off', ...args], {
    cwd: root,
    encoding: 'utf8',
    maxBuffer: 512 * 1024 * 1024,
    stdio: ['ignore', 'pipe', 'pipe'],
  });
}

function tryGit(root, args) {
  try {
    return git(root, args).trim();
  } catch {
    return null;
  }
}

const DIFF_FLAGS = ['--no-color', '--no-ext-diff', '-U0', '--diff-filter=ACMR'];

const refExists = (root, ref) =>
  tryGit(root, ['rev-parse', '--verify', '--quiet', `${ref}^{commit}`]) !== null;

/** The default base: the current release branch, else the newest one, else release.baseBranch. */
export function resolveBase(root, config) {
  const { branchPrefix, current, baseBranch } = config.release;
  const names = [];
  if (current) names.push(`${branchPrefix}${current}`);
  else {
    const heads = tryGit(root, [
      'for-each-ref',
      '--sort=-committerdate',
      '--format=%(refname:short)',
      'refs/heads/',
    ]);
    const newest = heads?.split('\n').find((name) => name.startsWith(branchPrefix));
    if (newest) names.push(newest);
  }
  names.push(baseBranch);
  for (const name of names) {
    for (const ref of [name, `origin/${name}`]) if (refExists(root, ref)) return ref;
  }
  return null;
}

/** Untracked, not ignored files as all-added lines. Binary and very large files: path only. */
function untracked(root) {
  const lines = [];
  const files = [];
  const list = git(root, ['ls-files', '--others', '--exclude-standard', '-z']);
  for (const file of list.split('\0').filter(Boolean)) {
    files.push({ file });
    const path = join(root, file);
    if (!existsSync(path) || statSync(path).size > MAX_FILE_BYTES) continue;
    const buffer = readFileSync(path);
    if (buffer.subarray(0, 8000).includes(0)) continue;
    buffer
      .toString('utf8')
      .split('\n')
      .forEach((text, index) =>
        lines.push({ file, line: index + 1, text: text.replace(/\r$/, '') }),
      );
  }
  return { lines, files };
}

/** Gathers the added lines for a scope: "staged", "uncommitted" or "base". */
export function collect(root, { scope, base, history }) {
  const parts = [];
  let described;
  if (scope === 'staged') {
    parts.push(parseDiff(git(root, ['diff', '--cached', ...DIFF_FLAGS])));
    described = 'staged changes';
  } else if (scope === 'uncommitted') {
    const head = refExists(root, 'HEAD') ? 'HEAD' : EMPTY_TREE;
    parts.push(parseDiff(git(root, ['diff', ...DIFF_FLAGS, head])), untracked(root));
    described = 'uncommitted changes';
  } else {
    const mergeBase = git(root, ['merge-base', base, 'HEAD']).trim();
    parts.push(parseDiff(git(root, ['diff', ...DIFF_FLAGS, mergeBase])), untracked(root));
    if (history) {
      const log = git(root, [
        'log',
        '-p',
        '--no-merges',
        '--format=commit:%H',
        ...DIFF_FLAGS,
        `${mergeBase}..HEAD`,
      ]);
      parts.push(parseDiff(log));
    }
    described = `changes since ${base}${history ? ' (and each commit)' : ''}`;
  }
  return {
    described,
    lines: parts.flatMap((part) => part.lines),
    files: parts.flatMap((part) => part.files),
  };
}

/** Runs a scan for a scope and applies the project's `secretsScan` settings. */
export function run(root, config, options) {
  const settings = mergeConfig(structuredClone(DEFAULTS.secretsScan), config.secretsScan || {});
  const collected = collect(root, options);
  const ignored = (entry) => settings.ignorePaths.some((glob) => matchesGlob(entry.file, glob));
  const ownEmail = tryGit(root, ['config', 'user.email']);
  const findings = scan(
    {
      lines: collected.lines.filter((e) => !ignored(e)),
      files: collected.files.filter((e) => !ignored(e)),
    },
    {
      disable: settings.disable,
      allow: settings.allow.map((source) => new RegExp(source)),
      ownEmails: new Set(ownEmail ? [ownEmail.toLowerCase()] : []),
    },
  );
  // The same value in the same file is reported once; prefer the working-tree occurrence.
  const seen = new Set();
  const unique = [];
  for (const finding of [...findings].sort((a, b) => Number(!!a.commit) - Number(!!b.commit))) {
    const key = `${finding.file}|${finding.rule}|${finding.fingerprint}`;
    if (!seen.has(key)) {
      seen.add(key);
      unique.push(finding);
    }
  }
  unique.sort(
    (a, b) =>
      SEVERITIES.indexOf(a.severity) - SEVERITIES.indexOf(b.severity) ||
      a.file.localeCompare(b.file) ||
      a.line - b.line,
  );
  return { scope: collected.described, scannedLines: collected.lines.length, findings: unique };
}

export const isBlocking = (finding) => finding.severity !== 'low';

export function formatFindings(findings) {
  return findings
    .map((f) => {
      const where = `${f.file}${f.line ? `:${f.line}` : ''}`;
      const commit = f.commit ? ` (only in commit ${f.commit.slice(0, 7)})` : '';
      return `  ${f.severity.toUpperCase().padEnd(6)} ${where}${commit}  ${f.label}${f.preview ? `  ${f.preview}` : ''}`;
    })
    .join('\n');
}

const ADVICE = `Secrets: move them to environment variables or a secret store; if one was ever pushed, rotate it.
Personal data: replace it with synthetic data (e.g. jane@example.com, 555-0100).
False positive: add "secrets-scan: allow" in a comment on the line, or a regex for the value to
secretsScan.allow in .claude/workflow.json.`;

/* ---- Claude Code hook ---- */

/** Whether a shell command runs `git commit` (also `git -C dir commit`, `... && git commit`). */
export const isGitCommit = (command) =>
  /(?:^|[;&|(\s])git(?:\s+-[cC]\s+\S+|\s+--?[\w-]+(?:=\S+)?)*\s+commit\b/.test(command);

/** PreToolUse decision for a tool call, or null to let it through. */
export function hookDecision(payload, scanFn) {
  const command = payload?.tool_input?.command;
  if (typeof command !== 'string' || !isGitCommit(command)) return null;
  const root = repoRoot(payload.cwd || process.cwd());
  const config = loadConfig(root);
  if (config.secretsScan === false || config.secretsScan?.commitHook === false) return null;
  const blocking = scanFn(root, config).findings.filter(isBlocking);
  if (!blocking.length) return null;
  const high = blocking.some((f) => f.severity === 'high');
  const reason = [
    `secrets-scan: the uncommitted changes add ${blocking.length} likely secret(s) or personal data:`,
    formatFindings(blocking),
    high
      ? 'Commit blocked. Remove these from the change (environment variables, synthetic data). If one is a false positive, ask the user before allowlisting it ("secrets-scan: allow" on the line, or secretsScan.allow in .claude/workflow.json). The secrets-scanner agent can review the change in full.'
      : 'Approve only if these are intended (for example synthetic test data).',
  ].join('\n');
  return {
    hookSpecificOutput: {
      hookEventName: 'PreToolUse',
      permissionDecision: high ? 'deny' : 'ask',
      permissionDecisionReason: reason,
    },
  };
}

/* ---- CLI ---- */

export function parseArgs(argv) {
  const options = { scope: 'base', base: null, history: false, json: false, hook: false };
  for (let i = 0; i < argv.length; i += 1) {
    const arg = argv[i];
    if (arg === '--staged') options.scope = 'staged';
    else if (arg === '--uncommitted') options.scope = 'uncommitted';
    else if (arg === '--base') {
      options.scope = 'base';
      options.base = argv[(i += 1)];
      if (!options.base) throw new Error('--base needs a ref');
    } else if (arg === '--history') options.history = true;
    else if (arg === '--json') options.json = true;
    else if (arg === '--hook') options.hook = true;
    else if (arg === '--root') options.root = argv[(i += 1)];
    else if (arg === '--help' || arg === '-h') options.help = true;
    else throw new Error(`unknown argument: ${arg}`);
  }
  return options;
}

function main() {
  const options = parseArgs(process.argv.slice(2));
  if (options.help) {
    const source = readFileSync(new URL(import.meta.url), 'utf8');
    console.log(
      /\/\*\*([\s\S]*?)\*\//
        .exec(source)[1]
        .replace(/^ \* ?/gm, '')
        .trim(),
    );
    return 0;
  }

  if (options.hook) {
    try {
      const payload = JSON.parse(readFileSync(0, 'utf8'));
      const decision = hookDecision(payload, (root, config) =>
        run(root, config, { scope: 'uncommitted' }),
      );
      if (decision) process.stdout.write(JSON.stringify(decision));
    } catch (error) {
      // Never break the user's commit because the scanner itself failed.
      console.error(`secrets-scan hook skipped: ${error.message}`);
    }
    return 0;
  }

  const root = options.root ?? repoRoot();
  const config = loadConfig(root);
  if (options.scope === 'base' && !options.base) {
    options.base = resolveBase(root, config);
    if (!options.base) throw new Error('no base branch found; pass --base <ref>');
  }
  const result = run(root, config, options);
  const blocking = result.findings.some(isBlocking);
  if (options.json) {
    console.log(JSON.stringify({ ...result, blocking }, null, 2));
  } else if (!result.findings.length) {
    console.log(
      `secrets-scan: no findings in ${result.scannedLines} added lines (${result.scope}).`,
    );
  } else {
    console.log(
      `secrets-scan: ${result.findings.length} finding(s) in ${result.scannedLines} added lines (${result.scope}):`,
    );
    console.log(formatFindings(result.findings));
    if (blocking) console.log(`\n${ADVICE}`);
  }
  return blocking ? 1 : 0;
}

if (process.argv[1]?.endsWith('secrets-scan.mjs')) {
  try {
    process.exitCode = main();
  } catch (error) {
    console.error(`secrets-scan: ${error.message}`);
    process.exitCode = 2;
  }
}
