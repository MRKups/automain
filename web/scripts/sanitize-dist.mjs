import { readdirSync, readFileSync, statSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';

function sanitizeDir(dir) {
  for (const entry of readdirSync(dir)) {
    const fullPath = join(dir, entry);
    const st = statSync(fullPath);
    if (st.isDirectory()) {
      sanitizeDir(fullPath);
      continue;
    }
    const raw = readFileSync(fullPath, 'utf8');
    const cleaned = raw.replace(/[\u2014\u2013]/g, '-');
    if (cleaned !== raw) {
      writeFileSync(fullPath, cleaned, 'utf8');
    }
  }
}

sanitizeDir(new URL('../dist', import.meta.url).pathname);
