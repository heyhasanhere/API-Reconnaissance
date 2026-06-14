# Session log — chronological

This is a chronological log of everything that happened in the original
chat, in the order it happened. It is **not** a design doc — see
`PLAN.md` for the planned redesign, and the work-in-progress is in
`/internal/...` Go source. This file is for picking up where we left
off in a new chat.

Project location: `~/Desktop/Project/API-Recon/` (originally created at
`~/Desktop/Find-API-Endpoint/`, then user moved it).
Module: `github.com/falcon/mine` (declared in `go.mod`).

---

## 0. Origin: anikage.cc API debugging

User had a question about an anikage.cc API endpoint. They pasted two
URLs and asked why one worked and the other returned 500.

```
GET https://anikage.cc/api/media/anime/zMLNvt6MtV/episodes
  → 200 OK, JSON array of 28 episodes for Frieren S1

GET https://anikage.cc/api/media/anime/zMLNvt6MtV/episodes/1/downloads
  → 500 {"success":false,"error":{"status":500,"message":"No episodes
     found for provider pahe"}}
```

The slug `zMLNvt6MtV` is anikage's internal ID for the anime "Frieren:
Beyond Journey's End" (Season 1, MAL ID 52991, AniList ID 154587).

## 1. Discovered the API actually works (just not /downloads)

I ran `curl` probes against several URL variations. The findings:

- `/episodes` → 200, returns list of episodes with `id` (UUID), `number`,
  `title`, `img`, etc.
- `/episodes/1/sources?provider=pahe` → 200, returns m3u8 key + subtitles
- `/episodes/1/sources?provider=miko&lang=sub` → 200 (newer provider)
- `/episodes/1/downloads` → 500 "No episodes found for provider pahe"
  (regardless of `?provider=` query param — it's hardcoded to "pahe")

The `pahe` in the error is the **provider name** anikage is trying to
use, not a query param the user should send. The error is server-side:
the pahe download indexer has no episode data for this slug.

Different error message surfaced later was the key clue:

```
/episodes/1/sources  → "provider query param is required (e.g. ?provider=zoro)"
```

This told us `/sources` is provider-aware; `/downloads` is not.

## 2. Mitmproxy + Playwright investigation

User asked how to use DevTools for network capture. I explained, then
user said "you do it" — so I installed mitmproxy, set up Playwright
with proxy at `127.0.0.1:8080`, drove a headless browser through the
proxy, and captured traffic.

Installed: `brew install mitmproxy` (v12.2.3), `playwright` via npm,
`chromium` via `npx playwright install chromium` (1223).

Key Playwright config that bypassed CA install:
```js
chromium.launch({
  headless: true,
  proxy: { server: 'http://127.0.0.1:8080' },
  args: ['--ignore-certificate-errors'],
})
context.newContext({ ignoreHTTPSErrors: true })
```

Findings:
- Watch page URL is `/anime/watch/{slug}?ep={n}`, NOT `/watch/{slug}`
  (the latter 404s)
- Slug is in URL path, e.g. `/anime/watch/zMLNvt6MtV?ep=1`
- Frontend calls these on page load:
  - `GET /api/media/anime/{slug}/episodes` — episode list
  - `GET /api/media/anime/{slug}/episodes/1/servers` — server list
  - `GET /api/media/anime/{slug}/episodes/1/sources?provider=miko&lang=sub`
- Server list revealed providers: `kiss`, `miko`, `verse`
- UI buttons also showed: `koto`, `e-kiss`, `e-aki`
- An ad div with `id="dontfoid"` was blocking Playwright clicks — fixed
  with `addInitScript` that removes `[id*="dontfoid"]` and `[id*="ad-"]`
  via MutationObserver
- Real Download button is `button.action-btn` (button[10] on the page)
- The frontend clicking Download fires:
  `GET /api/media/anime/{slug}/episodes/1/downloads` — same 500 error
  as the user's original. The downloads endpoint is just broken for
  this slug.

## 3. Stream proxy discovery

The "url" returned by `/sources` is not an actual URL — it's a key
into `prox.anikage.cc`, anikage's CDN proxy.

```
GET https://prox.anikage.cc/m3u8/{encoded_key}
  → 200 application/vnd.apple.mpegurl
  → master m3u8 with one variant

GET https://prox.anikage.cc/stream/{other_key}    (or /stream/{same_key} with small variation)
  → variant m3u8 with segment list
  → segments at /stream/{encoded}? varies by segment
```

Critical: requests to `prox.anikage.cc` require these headers or they
403:
```
Origin: https://anikage.cc
Referer: https://anikage.cc/
```

The variant m3u8 returns 200 with proper `application/vnd.apple.mpegurl`
content-type when these headers are sent. Without them: `403 forbidden
origin` (text/plain) or `400 missing payload`.

The master m3u8 format from `prox.anikage.cc/m3u8/{X}`:
```
#EXTM3U
#EXT-X-VERSION:3
#EXT-X-STREAM-INF:BANDWIDTH=25481669,AVERAGE-BANDWIDTH=3793533,RESOLUTION=1920x1080,CODECS="avc1.640028,mp4a.40.2"
/stream/{encoded_variant_key}
```

The variant m3u8 from `/stream/{X}`:
```
#EXTM3U
#EXT-X-VERSION:3
#EXT-X-TARGETDURATION:12
#EXT-X-MEDIA-SEQUENCE:0
#EXT-X-PLAYLIST-TYPE:VOD
#EXTINF:4.295956,
/stream/{segment_key_1}
#EXTINF:6.798467,
/stream/{segment_key_2}
...
```

Note: same encoded key produces different responses at `/m3u8/` vs
`/stream/`. The `/m3u8/` is the master; `/stream/` is the variant.
Same key in variant segments has small character variations (a single
byte change in the encoded value).

## 4. Tools comparison

Tried to download with ffmpeg — failed with:
```
[http @ ...] is not in allowed_segment_extensions, consider updating hls.c
[http @ ...] Error opening input: Invalid data found when processing input
```

ffmpeg's HLS demuxer requires segment URLs to have `.ts` or `.m4s`
extensions. The flag `-allowed_extensions ALL` is silently ignored in
ffmpeg 8.1.1. **ffmpeg is the wrong tool for HLS segments without
extensions.**

yt-dlp handles them natively. First attempt with default settings:
- Throughput: ~700 KB/s
- 25-min episode would have taken ~15 min
- Worked but slow

## 5. Parallel download with yt-dlp

The killer flag: `--concurrent-fragments 16`. Speed went from 700 KB/s
to 15.39 MiB/s — **20× speedup**. 651 MB episode downloaded in 42s.

Final result: `/tmp/frieren_ep1.mp4` — 1920x1080, H.264, AAC 44.1kHz,
24 minutes, 664 MB.

This proved the server throttles **per-connection**, not per-IP. Same
data with `-c 1` vs `-c 16` would have shown linear scaling.

## 6. Working download script (saved as /tmp/anikage_dl.sh)

```bash
#!/bin/bash
set -e
SLUG="${1}"
EP="${2}"
PROVIDER="${3:-miko}"
LANG="${4:-sub}"
OUT="${5:-${SLUG}_ep${EP}.mp4}"

# 1. Get encoded stream key
ENCODED=$(curl -s "https://anikage.cc/api/media/anime/${SLUG}/episodes/${EP}/sources?provider=${PROVIDER}&lang=${LANG}" \
  | python3 -c 'import json,sys; print(json.load(sys.stdin)["sources"][0]["url"])')

# 2. Get master playlist (or fall through to variant)
curl -s -H "Origin: https://anikage.cc" -H "Referer: https://anikage.cc/" \
  "https://prox.anikage.cc/m3u8/$ENCODED" -o /tmp/master.m3u8

if grep -q "EXT-X-STREAM-INF" /tmp/master.m3u8; then
  STREAM_PATH=$(awk '/^\/stream/' /tmp/master.m3u8 | head -1)
  PLAYLIST_URL="https://prox.anikage.cc$STREAM_PATH"
else
  PLAYLIST_URL="https://prox.anikage.cc/stream/$ENCODED"
  curl -s -H "Origin: https://anikage.cc" -H "Referer: https://anikage.cc/" \
    "$PLAYLIST_URL" -o /tmp/master.m3u8
fi

# 3. yt-dlp with parallel fragments
yt-dlp \
  --add-header "Origin:https://anikage.cc" \
  --add-header "Referer:https://anikage.cc/" \
  --concurrent-fragments 16 \
  --no-warnings \
  -o "$OUT" \
  "$PLAYLIST_URL"
```

## 7. User asked: write a lean project that mines knowledge following this process

I built `mine` (originally in `~/Desktop/Find-API-Endpoint/`, now
`~/Desktop/Project/API-Recon/`). Subcommands:

| Subcommand | What it does |
|---|---|
| `probe` | Single HTTP request, shows status/headers/timing/body. Has `--json` for piping. |
| `diff` | Fetch two URLs, show header + body diff with first-divergent byte highlighted. |
| `watch` | Headless browser (Playwright via Node helper), captures all traffic as JSONL. Has `--filter` (regex) and `--out` (file). |
| `click` | Open URL, click a CSS selector, log only the requests triggered by the click. |
| `flood` | Parallel request flood with `-c` (concurrency) and `-n` (total). Reports req/s, MB/s, status codes, p50/p90/p99/max latency. |
| `record` | Read JSONL session, emit runnable script. Templates: `curl`, `yt-dlp`, `bash`. |

All 6 verified working. The 11 MB Go binary uses stdlib + Playwright
(via Node helper) + a tiny `parseflags` shim that lets flags go anywhere
in argv.

## 8. User pushed back: too hard to use

User said: "very hard to understand. I can not understand what is doing
what and for what purpose and what answer I am looking for using that
options or tool or command."

Specific complaints:
- Options like `--filter api/` and `--out` weren't documented well
- The user has to know which subcommand + which flags + in what order
- An interactive tool would be better

I asked clarifying questions:
1. UX style → user chose **Guided REPL** (also offered single-shot
   auto-detect and "both")
2. How options shown → user chose **Both** (per-option descriptions
   AND live suggestions)
3. Suggestions source → user chose **Both** (heuristics + playbooks)

## 9. Approved redesign plan

Wrote a redesign plan to `~/.puku-cli/plans/tidy-mixing-ritchie.md`
(same path as the old plan, overwritten). Plan was approved.

Redesign summary:
- Default to REPL when no subcommand: `mine` or `mine <url>`
- Numbered menu with one-line descriptions and examples
- Each option is annotated with what it tells you and what to look for
- After each action, REPL suggests next step (heuristics)
- Ship with playbooks: video-streaming, auth-login, paged-data, broken-api
- Auto-generated help from a single `Action` type so docs can't lie
- Old subcommand syntax still works (back-compat)

## 10. Plan file at `~/Desktop/Project/API-Recon/.context/PLAN.md`

I had already written the plan to `.context/PLAN.md` before the user
asked for the log. PLAN.md is the design doc; this file (LOG.md) is
the chronological record.

## 11. Created the `TaskCreate` tasks but got interrupted

I had just started creating tasks for the redesign when the user
interrupted to ask for this log. No code has been written for the
redesign yet — only the plan exists.

The tasks I was about to create (in order):
1. Design `Action` type in `pkg/action/action.go`
2. Refactor subcommands to expose `Action` struct
3. Build REPL loop, menu, prompt in `internal/repl/repl.go`
4. Add heuristic suggestions in `internal/repl/suggest.go`
5. Add built-in playbooks in `internal/repl/playbooks.go`
6. Wire `main.go` to launch REPL by default

## 12. State of the codebase as of interruption

`/Users/falcon/Desktop/Project/API-Recon/`:
- `go.mod` — `module github.com/falcon/mine`, go 1.26.4
- `main.go` — subcommand dispatch (will be replaced with REPL bootstrap)
- `README.md` — original subcommand-style docs (will be rewritten)
- `examples/anikage.md` — worked example (will be rewritten as REPL
  transcript)
- `internal/probe/probe.go` — works, returns text or JSON
- `internal/diff/diff.go` — works
- `internal/flood/{flood.go,json.go}` — works, reports throughput +
  status histogram + latency percentiles
- `internal/record/record.go` — works, has curl/yt-dlp/bash templates
- `internal/watch/watch.go` — works, shells out to Node helper
- `internal/watch/node/helper.mjs` — Playwright helper, JSONL output
- `internal/click/click.go` — works, same helper with `--click` flag
- `pkg/parseflags/parseflags.go` — lets flags go anywhere in argv

The Node helper in `internal/watch/node/` symlinks to
`/tmp/node_modules` for Playwright. User will need to do `npm install
playwright && npx playwright install chromium` if `/tmp/node_modules`
is gone.

Binary: built at `/tmp/mine` (11 MB). Build command: `go build -o mine .`

Known bugs fixed during development:
- `statusMap` in flood.go originally printed pointers; fixed to iterate
  and print int → int
- `record --template yt-dlp` was hardcoded to say "curl" in its
  comment; fixed via `tplData.Template` field
- Go's `flag` package requires flags before positionals; fixed with
  `parseflags.Split`

## 13. Where to resume in a new chat

Open `~/Desktop/Project/API-Recon/.context/LOG.md` (this file) and
`.context/PLAN.md` (the design). The two together give full context:
this file is "what happened," PLAN.md is "what to build next."

The new chat should:
1. Read both files
2. Implement the REPL per PLAN.md
3. Refactor each subcommand to expose an `Action` struct
4. Wire main.go to default to REPL when no subcommand given
5. Add playbooks: video-streaming, auth-login, paged-data, broken-api
6. Rewrite `examples/anikage.md` as a REPL transcript

The user moved the project from `~/Desktop/Find-API-Endpoint/` to
`~/Desktop/Project/API-Recon/`. Old chat referenced the old path; if
any absolute paths in this log or the code are wrong, check both.

## 14. Scope change — knowledge-mining tool, not a flashlight

User re-scoped the project after reviewing PLAN.md v1. The original plan
built a guided REPL wrapper around the existing subcommands (a friendlier
flashlight). The new direction is a **stateful knowledge-mining tool** with
two phases:

- **Discovery (active)** — given a starting point, find things of value.
  When standard methods don't yield, escalate like a red-team operator.
- **Replay (passive, daily-use)** — once captured, one command runs the
  full flow. The recipe is the source of truth; scripts are generated
  output.

The unit of reuse is a **recipe**: JSON file keyed by domain describing
endpoints, captured headers, sibling relationships, and the download
shape. Recipes are stored in `~/.local/share/mine/recipes/`.

### v1 capabilities (in dependency order)

1. **Response shape classifier (A)** — recognizes what a response is by
   content-type, structure, and error message text. Powers the rest.
2. **Token / credential capture (B)** — flags and stores `Authorization`,
   `Cookie`, `X-API-Key`, `Set-Cookie`. Injects on replay. Critical for
   recipe durability.
3. **Endpoint graph (C)** — builds sibling relationships. The unlock for
   the anikage 500 → workaround transition.
4. **Informed fuzzing (F)** — tries values named in error messages, the
   page UI, sibling requests. Highest-value heuristic.
5. **Download interpreter (H)** — first-class command that takes any
   recognized source (HLS, DASH, direct, segment list, HTML page) and
   produces a working download with captured auth. **Not a template.**
6. **Recipe store** — JSON files keyed by domain. `mine harvest`, `mine
   run`, `mine ls`, `mine show`, `mine verify`, `mine recipe edit`.
7. **REPL** — front door to `harvest` for first-time discovery. After
   onboarding, the user runs `mine run`, not the REPL.

### Explicitly out of scope for v1

- Full side-channel crawler (G full) — parts we needed fall out of (1)
- Live verification drift UI (E) — `verify` is a small command; the
  polished report is v2
- Negative knowledge dashboard (D full) — failed-attempt notes have a
  place in the recipe from day one; cross-domain learning is v2
- Collaborative / shareable recipes — solo vs shared is a fork, defer
- History diff (response changed since last visit) — needs history first,
  free with `verify` later

### Why this is better than the old plan

The old plan built a nicer flashlight. The new plan builds a tool that
remembers, replays, adapts to any source, heals via `verify`, and teaches
via heuristic suggestions.

The full revised plan is in `PLAN.md` (overwritten). Build order is at
the bottom of PLAN.md.

## 15. Environment notes (in case `/tmp` is wiped)

- `/tmp/anikage_dl.sh` — the working download script
- `/tmp/test-session.jsonl` — fake session for record testing
- `/tmp/frieren_ep1.mp4` — downloaded 651MB Frieren episode 1
- `/tmp/mine` — built binary
- `/tmp/node_modules/` — Playwright install (symlinked into project)
- `~/.mitmproxy/` — mitmproxy CA cert (`mitmproxy-ca-cert.pem`)
- Homebrew: `go`, `ffmpeg`, `yt-dlp`, `aria2`, `mitmproxy` all installed
- Anikage slugs for Frieren S1: `zMLNvt6MtV` (this is the one in the
  original question), S2: `VZMsgTFvHC`, S3: `S5u5bVkTdh`
- A different working slug: `FdWSYnDxFW` (Dr. STONE)
