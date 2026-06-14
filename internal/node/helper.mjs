// internal/node/helper.mjs
//
// Playwright helper for api-recon. Drives a headless Chromium
// instance and emits JSONL of all observed requests and responses
// to stdout. The Go side (internal/capture) reads this line-by-line
// and feeds each line to creds/graph/shape.
//
// Usage:
//   node internal/node/helper.mjs watch <url> [--filter <regex>] [--click <selector>] [--wait <ms>] [--no-ads] [--save <dir>]
//
// JSONL schema (versioned with `v` for forward-compat):
//   {"v":1,"dir":"req","ts":<epoch_ms>,"method":"GET","url":"...","headers":{...},"body":"<base64>"}
//   {"v":1,"dir":"resp","ts":<epoch_ms>,"status":200,"url":"...","headers":{...},"body":"<base64>"}

import { chromium } from 'playwright';
import { writeFileSync, mkdirSync, existsSync } from 'node:fs';
import { resolve } from 'node:path';

const VERSION = 1;

// ---- argv parsing ----
function parseArgs(argv) {
  const opts = {
    mode: 'watch',
    url: '',
    filter: null,
    click: null,
    wait: 0,
    noAds: true,
    save: null,
  };
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i];
    if (a === 'watch' || a === 'click') { opts.mode = a; continue; }
    if (a.startsWith('--')) {
      const key = a.slice(2);
      const next = argv[i + 1];
      if (key === 'filter') { opts.filter = new RegExp(next); i++; }
      else if (key === 'click') { opts.click = next; i++; }
      else if (key === 'wait') { opts.wait = parseInt(next, 10) || 0; i++; }
      else if (key === 'no-ads') { opts.noAds = true; }
      else if (key === 'save') { opts.save = next; i++; }
    } else if (!opts.url) {
      opts.url = a;
    }
  }
  return opts;
}

const opts = parseArgs(process.argv.slice(2));
if (!opts.url) {
  process.stderr.write('helper: URL is required\n');
  process.exit(2);
}

const re = opts.filter;

// emit writes one JSONL record to stdout, flushed. We use
// process.stdout.write so the Go side can parse line-by-line
// in real time.
function emit(record) {
  record.v = VERSION;
  process.stdout.write(JSON.stringify(record) + '\n');
}

// ---- ad blocker ----
const AD_INIT_SCRIPT = `
  (() => {
    const observer = new MutationObserver((mutations) => {
      for (const m of mutations) {
        for (const n of m.addedNodes) {
          if (!(n instanceof HTMLElement)) continue;
          const id = n.id || '';
          if (id.includes('dontfoid') || id.startsWith('ad-')) {
            n.remove();
          }
          for (const child of n.querySelectorAll('[id*="dontfoid"], [id^="ad-"]')) {
            child.remove();
          }
        }
      }
    });
    observer.observe(document.documentElement, { childList: true, subtree: true });
  })();
`;

// ---- main ----
const browser = await chromium.launch({ headless: true });
const context = await browser.newContext({ ignoreHTTPSErrors: true });
const page = await context.newPage();

if (opts.noAds) {
  await page.addInitScript({ content: AD_INIT_SCRIPT });
}

// Wire up request/response capture.
page.on('request', (req) => {
  if (re && !re.test(req.url())) return;
  const headers = req.headers();
  const body = req.postData();
  emit({
    dir: 'req',
    ts: Date.now(),
    method: req.method(),
    url: req.url(),
    headers,
    body: body ? Buffer.from(body).toString('base64') : null,
  });
});

page.on('response', async (resp) => {
  if (re && !re.test(resp.url())) return;
  const headers = resp.headers();
  let body = null;
  try {
    const buf = await resp.body();
    if (buf) {
      body = Buffer.from(buf).toString('base64');
      if (opts.save) {
        saveBody(resp.url(), buf);
      }
    }
  } catch (e) {
    // Some responses (e.g. data: URLs) can't be read; ignore.
  }
  emit({
    dir: 'resp',
    ts: Date.now(),
    status: resp.status(),
    url: resp.url(),
    headers,
    body,
  });
});

function saveBody(url, buf) {
  if (!existsSync(opts.save)) {
    mkdirSync(opts.save, { recursive: true });
  }
  // Hash the URL into a filename. Use a simple deterministic
  // approach: replace non-alphanumerics with underscores.
  const name = url.replace(/[^a-zA-Z0-9._-]/g, '_').slice(0, 200);
  try {
    writeFileSync(resolve(opts.save, name), buf);
  } catch (e) {
    // Best-effort: don't crash on save failure.
  }
}

// Navigate.
try {
  await page.goto(opts.url, { waitUntil: 'load', timeout: 30000 });
} catch (e) {
  emit({ dir: 'error', ts: Date.now(), message: 'goto failed: ' + e.message });
}

if (opts.click) {
  try {
    await page.click(opts.click, { timeout: 10000 });
  } catch (e) {
    emit({ dir: 'error', ts: Date.now(), message: 'click failed: ' + e.message });
  }
}

if (opts.wait > 0) {
  await new Promise((r) => setTimeout(r, opts.wait));
}

await browser.close();
process.exit(0);
