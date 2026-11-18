/**
 * Minimal glob support (no dependencies): `**` matches any number of folders, `*` anything
 * except "/", `?` one character. Paths are relative to the root and use forward slashes.
 * node_modules, .git and common build folders are never searched.
 */
import { existsSync, readdirSync } from 'node:fs';
import { join } from 'node:path';

const IGNORED = new Set(['node_modules', '.git', 'dist', 'build', 'coverage', '.vite', '.next']);

/** Converts a glob to an anchored regular expression. */
export function globToRegExp(glob) {
  let pattern = '';
  for (let i = 0; i < glob.length; i += 1) {
    const char = glob[i];
    if (char === '*' && glob[i + 1] === '*') {
      const slash = glob[i + 2] === '/';
      pattern += slash ? '(?:.*/)?' : '.*';
      i += slash ? 2 : 1;
    } else if (char === '*') pattern += '[^/]*';
    else if (char === '?') pattern += '[^/]';
    else pattern += char.replace(/[.+^${}()|[\]\\]/g, '\\$&');
  }
  return new RegExp(`^${pattern}$`);
}

export const matchesGlob = (path, glob) => globToRegExp(glob).test(path);

/** Files under `root` matching `glob`, as sorted relative paths. */
export function findFiles(root, glob) {
  const regex = globToRegExp(glob);
  // Only walk below the glob's literal leading folders.
  const literal = glob.split('/');
  const start = [];
  while (literal.length > 1 && !/[*?]/.test(literal[0])) start.push(literal.shift());
  const found = [];
  const walk = (relative) => {
    const dir = join(root, relative);
    if (!existsSync(dir)) return;
    for (const entry of readdirSync(dir, { withFileTypes: true })) {
      if (IGNORED.has(entry.name)) continue;
      const path = relative ? `${relative}/${entry.name}` : entry.name;
      if (entry.isDirectory()) walk(path);
      else if (regex.test(path)) found.push(path);
    }
  };
  walk(start.join('/'));
  return found.sort();
}
