# api-recon

Discover streaming APIs and download their media — automatically, adaptively, transparently.

`api-recon` takes a single URL on a video-streaming site, auto-discovers the
API chain (episodes → servers → sources → CDN URL → m3u8), surfaces every
relevant piece of information to the user (episode list, provider list,
required headers, sample stream URL, estimated sizes), and then downloads what
the user picks.

For anikage.cc, the single command `api-recon <anime-url>` shows: 28 episodes
with titles, 4 providers (`megg`, `kiss`, `miko`, `verse`), the fact that
`miko` returns an HLS playlist while `kiss` returns third-party embeds, the
`Origin`/`Referer` headers the CDN requires, and a sample resolved m3u8. The
user picks: which episode(s), which provider, and the tool downloads. The next
time, `api-recon run anikage.cc <slug> <ep>` skips discovery and replays the
saved recipe.

For unknown sites, the same flow applies but the discovery chain is built by
an adaptive strategy that pattern-matches on response shapes (list-with-id,
JSON-with-embedded-stream-key, cross-host URL, m3u8 vs DASH vs direct file,
403 forbidden-origin tell).

The example output below is a real captured trace from anikage.cc during the
2026-06-15 reconnaissance. The site has since been rewritten as a client-side
SPA, so the live URL may no longer be navigable — see "What happens when the
site changes" below. The shape of the tool's output is unchanged.

## Install

```bash
go install github.com/heyhasanhere/API-Reconnaissance@latest
```

You also need `yt-dlp` in your `$PATH`. `api-recon` shells out to it for
HLS, DASH, and direct downloads.

```bash
# macOS
brew install yt-dlp
# pip
pip install yt-dlp
# otherwise: https://github.com/yt-dlp/yt-dlp#installation
```

For segment-list downloads, `aria2c` is used as a fallback. `curl` is used if
neither is available.

## Quick start

```bash
# Discover + prompt + download (anikage example)
api-recon https://anikage.cc/anime/zMLNvt6MtV

# Same, but non-interactive (CI, pipes, scripts)
api-recon --episodes 1,3-5 --provider miko https://anikage.cc/anime/zMLNvt6MtV

# Resolve to a stream URL, don't download
api-recon --dry-run https://anikage.cc/anime/zMLNvt6MtV

# Or feed the tool the API resource URL directly — useful when
# the HTML entry page is JS-rendered and returns a shell, but
# the API root is still navigable.
api-recon --dry-run https://anikage.cc/api/media/anime/zMLNvt6MtV/episodes

# Replay a saved recipe (no discovery)
api-recon run anikage.cc zMLNvt6MtV 1

# List saved recipes
api-recon ls

# Show a saved recipe
api-recon show anikage.cc
```

## Usage

```
api-recon <url>                  # discover, prompt, download (default)
api-recon run <domain> [args]    # replay a saved recipe
api-recon ls                     # list saved recipes
api-recon show <domain>          # print a saved recipe

Flags:
  -concurrency N     concurrent fragments for HLS/DASH (default 16)
  -dry-run           discover and resolve the stream, but don't download
  -episodes RANGE    episode range (e.g. '1,3-5'); skips the prompt
  -h, -help          show help
  -json              machine-readable output
  -no-color          disable color output
  -no-repl           force subcommand mode even if a URL is given
  -output DIR        output directory for downloaded files
  -provider NAME     provider name; skips the prompt
  -store DIR         override recipe store directory
  -v, -version       print version
```

The default command is fully automatic: it runs the chain engine against
`<url>`, prints what it found, prompts the user for episode and provider
selection, then downloads.

In `--json` mode, all output is a single JSON document on stdout (episodes,
providers, headers, resolved stream URL, download results). In non-TTY mode
(pipes, CI), both prompts are skipped and the tool uses the first working
provider for all episodes.

## What the user sees — anikage end-to-end

```text
$ api-recon https://anikage.cc/anime/zMLNvt6MtV
Resolving https://anikage.cc/anime/zMLNvt6MtV
  ↳ entry URL is a list endpoint; API base = https://anikage.cc/api/media/anime/zMLNvt6MtV, list path = /episodes

Found 28 episodes:
   1. The Land Where Souls Rest
   2. ...
  28. ...

Found 4 providers:
  megg
  kiss
  miko
  verse

Stream resolved:
  URL:  https://prox.anikage.cc/m3u8/CQQGHwtDHR9BB1dDXBkRHVFRUV4EXhwKDEMKAQQATgAeDgFWUwICBFdARF5MLkoGUEUkAEEDKRMdRltSBB9cAghNDRZRXwVNQV1JTAcIBARhGAYbCAoIHx1BFgdcDhYQX1VVUU8fAAhX
  Kind: hls_variant
  Required headers:
    Origin: https://anikage.cc
    Referer: https://anikage.cc/

Select episode(s):
  [a] all 28   [1-12] first half   [1] single   custom: e.g. 1,3,5-8   [q] quit
> 1
Select provider:
  [miko]   (recommended — HLS, Origin/Referer required)
  [verse]  (direct MP4)
  [megg]   (different CDN, not HLS)
> miko
Downloading: ./The Land Where Souls Rest - 01.mkv (provider=miko)
  stream: https://prox.anikage.cc/m3u8/...
  ...

Recipe saved to ~/.local/share/api-recon/recipes/anikage.cc.json
```

## What `api-recon run` does

Once a recipe is saved, replay is one command and a few placeholders:

```bash
$ api-recon run anikage.cc zMLNvt6MtV 1
recipe loaded: 3 steps, 4 providers
applying Origin/Referer for prox.anikage.cc
fetching /episodes/1/sources?provider=miko&lang=sub → 200
resolving https://prox.anikage.cc/m3u8/{key} → 200 (variant)
Downloading: ./The Land Where Souls Rest - 01.mkv (provider=miko)
  ...
```

No prompts, no discovery — the saved chain runs end-to-end in seconds.

## Architecture

`api-recon` is split into 8 small, single-purpose packages. The big one is
`chain` (~700 lines) — the adaptive engine. Everything else is under 300
lines.

```
main.go              dispatcher: discover → prompt → download (or replay)
pkg/probe            single HTTP round-trip with body capture
pkg/classify         response shape classifier (json_list, hls_master, ...)
pkg/auth             per-host captured headers (Origin/Referer)
pkg/chain            adaptive chain engine — the 7-step brain
pkg/chain/strategies pure decision functions on observable signals
pkg/pick             interactive multi-select prompt
pkg/pickjson         URL query-param mutator for ?provider=
pkg/download         yt-dlp / aria2c subprocess with auth injection
pkg/recipe           minimal on-disk store (atomic write, mode 0600)
```

Total: ~4500 lines of Go, no runtime deps beyond the standard library.

## How the chain engine works

The engine runs a fixed sequence of strategy functions, each given a probe
and a `Discovery` state. Each step returns a decision based on observable
signals from the response — not hardcoded "if host is X" checks.

1. **ResolveEntry** — fetch the entry URL. If it's HTML, scan `<script>` for
   `/api/...` hints. If it's JSON, treat the entry URL as the start.
2. **DetectListEndpoint** — try common list suffixes (`/episodes`, `/items`,
   `/list`, `/catalog`, `/browse`, `/videos`, `/posts`). First non-empty
   JSON array wins.
3. **DrillIntoItem** — from the first item, derive a resource URL by trying
   candidate fields (`id`, `number`, `episode`, `slug`). If all 404, fall
   back to a synthetic base (anikage has no resource endpoint, only children).
4. **EnumerateSiblings** — probe common sibling suffixes (`/sources`,
   `/servers`, `/streams`, `/downloads`, `/subtitles`).
5. **EnumerateProviders** — if the sibling is a list of `{id, default?, ...}`,
   treat it as the provider list. If the sibling has a `sources: [...]`
   field, treat the first source as a single provider's response.
6. **ResolveStream** — for the chosen provider, the `url` field is either a
   real URL or a CDN key. If opaque, find the CDN host and try
   `<host>/m3u8/{key}` then `<host>/stream/{key}`.
7. **ClassifyPlaylist** — fetch the stream URL with captured headers. If
   HLS master, parse variants and pick the highest-bandwidth. If HLS variant,
   count segments and estimate size. If 403 forbidden-origin, inject
   `Origin`/`Referer` and retry.

### Signal table (decision inputs)

| Signal | Chain reaction |
|---|---|
| Top-level JSON array | The body is a list of resources. `id`/`number` field is the drill target. |
| 404 on UUID drill | `id` is wrong. Try `number`, then integer 1, 2, 3. |
| `sources: [{url, isM3U8, ...}]` | This is the play URL. Inspect each source's `isM3U8` and `embedUrl`. |
| `embedUrl` populated | Source is a third-party embed. Mark and skip. |
| `isM3U8: true` + opaque `url` | CDN key. Find the CDN host. Try `<host>/m3u8/{key}` then `<host>/stream/{key}`. |
| `403 forbidden origin` | CDN requires `Origin`/`Referer`. Set from the page host, retry. |
| `EXT-X-STREAM-INF` in m3u8 body | Master playlist. Parse variants, pick highest bandwidth. |
| `EXTINF` directly in m3u8 body | Variant playlist. Count segments, estimate size, done. |
| `content-type: image/jpeg` for a segment | CDN obfuscation. Trust the m3u8, ignore the content-type. |
| `400 provider query param is required` | Missing required param. Try `/servers` for the list. |
| `500 No episodes found for provider X` | Provider name in error message is real. Try it. |
| `5xx` with `success: false` envelope | Read the message, look for provider/path hints. |

The signals are all in `pkg/chain/strategies.go` as small functions that
take a `*Discovery` and a `Step` and return an `Action`
(`ContinueWithHint`, `TryAlternative`, `Stop`).

## The anikage trap

Anikage's API has a single non-obvious shape that v0.1.0's REPL got wrong
repeatedly. The episode list returns objects with both an `id` field (a
UUID) and a `number` field (an integer). The natural drill is to use `id`
as the path component — but the server returns 404 for any UUID-shaped
path. The `number` is what the server actually wants.

The chain engine handles this with a fallback chain: if `id` drill returns
404, try `number`. If that 404s, try the integer literal `1`. The
`strategies.go` file encodes this as a small `DecideDrill()` function that
examines the response status and the first-item's available fields.

A second non-obvious shape: the CDN sometimes returns `video/mp4` for an
m3u8 response (content-type spoofing). The classifier trusts the URL path
(`/m3u8/`) over the content-type, so anikage's stream is correctly
classified as `hls_variant`.

## Recipe store

Recipes live in `~/.local/share/api-recon/recipes/<domain>.json`, mode 0600,
atomic write. The chain engine calls `Save()` at the end of a successful
discovery. The user does not manage recipes; the tool does.

```json
{
  "schema_version": 1,
  "domain": "anikage.cc",
  "discovered": "2026-06-15T...",
  "chain": [
    {"name": "episodes", "url_template": "/api/media/anime/{slug}/episodes"},
    {"name": "servers",  "url_template": "/api/media/anime/{slug}/episodes/{n}/servers"},
    {"name": "sources",  "url_template": "/api/media/anime/{slug}/episodes/{n}/sources?provider={provider}&lang={lang}"}
  ],
  "providers": ["megg", "kiss", "miko", "verse"],
  "headers": {"Origin": "https://anikage.cc", "Referer": "https://anikage.cc/"},
  "stream_template": "https://prox.anikage.cc/m3u8/{key}"
}
```

Override the store location with `--store DIR` (useful for CI / Docker
where `~/.local/share` may not be writable).

## Cloudflare and bot detection

The probe sends a `User-Agent: api-recon/0.2.0` header by default. Anikage
(and most other anime sites) are behind Cloudflare with a default-allow
User-Agent policy. The default UA is enough to get past the challenge in
the vast majority of cases. If a site requires JavaScript rendering
(which neither anikage nor any other tested site does), `api-recon` will
fail with a clear message naming the URL it tried and the HTML response it
got.

## Honest disclosure

v0.2.0 does **not** support:
- **JavaScript-rendered pages.** No Playwright, no headless browser. If a
  site requires JS to render, the tool fails clearly with "entry URL
  returned HTML, not the expected JSON" and points at the URL.
- **Authenticated sessions.** No cookie capture, no `Authorization` header
  harvesting. If a site requires login, the tool surfaces this as a 401/403
  in step 1 and stops.
- **DASH/HLS variant selection.** The chain picks the highest-bandwidth
  variant from an HLS master; the user cannot pick a different one.
- **Audio-only / format selection.** Always MKV via `yt-dlp` default.
- **Concurrent downloads of multiple episodes.** Downloads are serial.
  This is by design — concurrent downloads from a CDN are a quick way to
  get rate-limited.

These are all roadmap items, not v0.2.0 scope.

### What happens when the site changes

Streaming sites change their URL patterns and API shapes regularly. When a
saved recipe stops working — e.g. the site moved to a SvelteKit SPA and the
old HTML entry page 404s — the tool fails clearly with a `last steps:`
trace showing exactly which URLs it tried and what each one returned. The
failure mode is intentional: the tool does not silently fall through to a
guess.

If the site is still partially reachable (e.g. the API root still works at
`/api/media/anime/<slug>` even though the HTML page is gone), you can run
`api-recon` against the API URL directly and it will skip step 1. If the
site has changed its API shape entirely, delete the saved recipe with
`rm ~/.local/share/api-recon/recipes/<domain>.json` and re-run discovery
from any working entry URL.

### A note on HLS pre-flight estimates

After a stream URL is resolved, the chain engine fetches the playlist once
to confirm it is HLS and to count segments for a size estimate. On CDNs
that obfuscate individual segments (returning `video/mp4` for `.ts`
requests, or using signed URLs that aren't fetchable without further
negotiation), the variant playlist may have an empty segment list and
the tool will report `segments=0 est_bytes=0`. The HLS classification is
still correct — yt-dlp will handle the actual download fine. The estimate
is purely a pre-flight convenience, not a contract.

## Development

```bash
go build ./...               # build
go test ./...                # all package tests
go test ./pkg/chain -v       # verbose chain engine tests
```

The test suite is 76 tests across 8 packages. Network-dependent tests
(`pkg/probe`) are gated by `httptest.NewServer` mocks, so `go test ./...`
is fully offline.

## Project layout

```
api-recon/
├── .context/                # PLAN.md, LOG.md (institutional memory)
├── go.mod                   # github.com/heyhasanhere/API-Reconnaissance
├── main.go                  # dispatcher (~960 lines)
├── pkg/
│   ├── probe/               # ~150 lines
│   ├── classify/            # ~580 lines (the kind table + sniffers)
│   ├── auth/                # ~150 lines
│   ├── chain/               # ~700 lines (the engine)
│   │   └── strategies.go    # ~150 lines (signal table)
│   ├── pick/                # ~250 lines
│   ├── pickjson/            # ~60 lines
│   ├── download/            # ~190 lines
│   └── recipe/              # ~275 lines
├── testdata/                # anikage_*.json fixtures
└── README.md
```

## License

MIT.
