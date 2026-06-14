# api-recon — knowledge-mining tool

## What this is

A stateful CLI that **discovers APIs, URLs, and downloadable sources** on a
target domain, **captures the recipe** (endpoints, headers, cookies, tokens,
sibling relationships) needed to call them, and **downloads files** from any
recognized source — all stored locally so a future invocation can replay the
discovery and skip the parts that don't change.

The unit of reuse is a **recipe**: a JSON file keyed by domain describing
"go here, capture this, call with these headers, download from this kind of
source." Recipes are the source of truth. Scripts are generated output.

## Origin: anikage.cc

The tool was built around debugging an anikage.cc Frieren episode download.
Walking that case against the design is how we know each capability is
load-bearing. See `LOG.md` for the full story. The short version:

- Probed endpoints, hit a 5xx, **read the error message literally** to
  discover the provider name was server-side
- Watched the frontend, captured `Origin` / `Referer` headers and the
  cross-host CDN (`prox.anikage.cc`)
- Discovered the m3u8 proxy required those headers or 403'd
- Switched from ffmpeg (couldn't handle extensionless segments) to yt-dlp
  with `--concurrent-fragments 16` (700 KB/s → 15 MB/s)

That whole flow must be reproducible by the tool.

## v1 scope

Build the minimum set that reproduces the anikage case end-to-end. The
capabilities are listed in dependency order — each one enables the next.

### 1. Response shape classifier (A)

The tool recognizes what a response *is* and reacts accordingly.

- `application/vnd.apple.mpegurl` → it's a stream. Inspect for master vs
  variant. Extract segment URL pattern. Offer to download.
- `application/json` with array of objects → it's a list endpoint. Try
  `?page=2`, `?offset=10`, `?cursor=...` to detect pagination.
- `Link: <...>; rel="next"` header → HATEOAS pagination. Walk the chain.
- 3xx → follow and record the redirect chain. Many "hidden" endpoints only
  show up after redirects.
- 5xx → mark as broken. Don't retry without reason.
- HTML with `<form>` → list action URL + method + field names as candidate
  API endpoint.
- HTML with `data-*` attributes or inline JSON → extract for side-channel
  discovery (see 7).
- `Set-Cookie` response → store as `new_session`.
- Error body with phrases like "provider {X} not found" → extract X as a
  possible alternative value to try. This is the "read the error
  literally" heuristic that cracked anikage.

This is the engine that powers sibling discovery (3), informed fuzzing (4),
and the download interpreter (5).

### 2. Token / credential capture (B)

`watch` and `click` flag and store credentials found in captured traffic:

- `Authorization: Bearer ...` → `bearer_token`
- `Cookie: session=...` → `session_cookie`
- `X-API-Key: ...` → `api_key`
- `Set-Cookie` responses → `new_session`

On replay, inject automatically. On 401/403, prompt: "got 401, the token
may be expired — try refreshing via [the auth endpoint we also captured]."

Without this, the recipe breaks the moment the server rotates a token.
Critical for the recipe to be durable.

### 3. Endpoint graph (C)

Every watch / click session builds a graph:

- Page A calls API X, Y, Z
- API X returns IDs that API Y accepts
- API Z is a sibling of API X (same path prefix, different last segment)

The graph is queryable: `api-recon graph <domain>` shows the shape. When the
tool discovers a new endpoint, it suggests siblings to try. This is what
turned anikage's 500-on-`/downloads` into "try `/sources` instead."

### 4. Informed fuzzing (F)

Not dumb fuzzing — fuzzing guided by what the response shape told us:

- Found `?provider=miko` works → try others named in error messages, the
  page UI, sibling requests. (The anikage case: `kiss`, `verse`, `koto`,
  `e-kiss`, `e-aki` came from UI buttons.)
- Found `/episodes/1` works → try `/episodes/0`, `/episodes/-1`,
  `/episodes/99999` for boundary behavior.
- Found slug is N chars, base64-ish → try length and character class
  variations.
- Found pagination param → try `page=0`, `page=-1`, `page=abc` to find
  edge cases.

The "try other values named in the error message" path is the highest-
value heuristic and falls out of (1) + (4).

### 5. Download interpreter (H)

A first-class command that takes any recognized source and produces a
working download command. Source shapes and their handlers:

- **HLS / m3u8** → yt-dlp with `--concurrent-fragments 16` (or higher),
  inject captured headers
- **DASH / mpd** → yt-dlp
- **Direct file (mp4, etc.)** → curl with parallel range requests, or
  aria2c
- **JSON list of segment URLs** → aria2c with input file
- **HTML page with download links** → extract, present, ask which
- **Any source requiring auth** → use captured session, not anonymous

The download command is **generated from the recognized shape + the
captured session**, not from a static template. Not a preset.

### 6. Recipe store

JSON files keyed by domain. Lives in `~/.local/share/api-recon/recipes/`
by default, with project-local `.api-recon/` overriding global.

Recipe shape (sketch):

```json
{
  "domain": "anikage.cc",
  "discovered": "2026-06-14",
  "endpoints": {
    "episodes": {
      "url": "https://anikage.cc/api/media/anime/{slug}/episodes",
      "method": "GET",
      "shape": "json_list"
    },
    "sources": {
      "url": "https://anikage.cc/api/media/anime/{slug}/episodes/{n}/sources",
      "method": "GET",
      "params": ["provider", "lang"],
      "shape": "stream_key"
    }
  },
  "siblings": {
    "/downloads": { "status": "broken", "since": "2026-06-14" }
  },
  "auth": {
    "required_headers": {
      "Origin": "https://anikage.cc",
      "Referer": "https://anikage.cc/"
    }
  },
  "cdn": {
    "host": "prox.anikage.cc",
    "inherits_auth": true
  },
  "download": {
    "shape": "hls",
    "tool": "yt-dlp",
    "flags": ["--concurrent-fragments", "16"]
  }
}
```

Commands:

- `api-recon harvest <url>` — guided discovery, writes a recipe
- `api-recon run <domain> [args]` — look up recipe, execute
- `api-recon ls` — list known domains
- `api-recon show <domain>` — print the recipe
- `api-recon verify <domain>` — re-run discovery steps against live target,
  report drift
- `api-recon recipe edit <domain>` — open in `$EDITOR`

### 7. REPL (front door to harvest)

`api-recon` or `api-recon <url>` launches a guided REPL. The REPL drives
`harvest` for first-time discovery of a domain. After onboarding, you
don't touch the REPL — you use `api-recon run`.

The REPL:

- Numbered menu with one question at a time
- Each option annotated: what it does, what to look for in the output
- After each action, suggests the next step based on heuristics (powered
  by the response shape classifier and the endpoint graph)
- Auto-generated help from a single `Action` type so docs can't lie
- Old subcommand syntax still works for back-compat

## Explicitly out of scope for v1

These are valuable but not required to reproduce the anikage case. They
become candidates for v2 after the foundation is solid and you've used
it enough to know what's actually missing.

- **Side-channel crawler (G full version)** — the parts we needed from
  it (cross-host URL discovery) fall out of response inspection in (1).
  The full version (sitemap.xml parsing, JS bundle analysis) is
  overshoot for v1.
- **Live verification drift UI (E)** — `verify` is a small command on
  top of the store. The polished "5 of 7 still work" report is v2.
- **Negative knowledge dashboard (D full version)** — "this didn't work"
  notes have a place in the recipe from day one, but there's no separate
  failed-attempts log or cross-domain learning in v1.
- **Collaborative / shareable recipes** — solo tool vs shared tool is a
  fork. Defer.
- **History diff (response changed since last visit)** — needs a history
  store first. Free with `verify` later.

## File changes (v1)

New:

- `pkg/shape/shape.go` — response shape classifier (1)
- `pkg/creds/creds.go` — token / credential capture and injection (2)
- `pkg/graph/graph.go` — endpoint graph builder and query (3)
- `pkg/fuzz/fuzz.go` — informed fuzzing strategies (4)
- `pkg/download/download.go` — download interpreter (5)
- `pkg/recipe/recipe.go` — recipe type, load, save, lookup
- `internal/store/store.go` — file-based store at `~/.local/share/api-recon/recipes/`
- `internal/harvest/harvest.go` — guided discovery driver
- `internal/repl/repl.go` — REPL loop, menu, prompt
- `internal/repl/suggest.go` — heuristic suggestions
- `internal/repl/help.go` — auto-generated help from Action structs
- `pkg/action/action.go` — the `Action` type that all subcommands implement

Refactored (keep `Run` for back-compat, add `Action` struct):

- `internal/probe/probe.go`
- `internal/diff/diff.go`
- `internal/watch/watch.go`
- `internal/click/click.go`
- `internal/flood/flood.go`
- `internal/record/record.go`

Wired:

- `main.go` — REPL by default when no subcommand given, subcommand
  dispatch as escape hatch
- `examples/anikage.md` — rewrite as REPL transcript showing `harvest`
  → `run` cycle

## Backward compatibility

Old subcommand syntax still works for scripts and muscle memory:

```bash
api-recon probe https://example.com         # still works
api-recon --json watch https://...          # still works
api-recon flood -c 16 -n 100 https://...    # still works
```

The REPL is the default when no subcommand is given. Power users keep
the old workflow.

## Verification

End-to-end test: the anikage case must be reproducible from scratch with
the tool alone, then re-runnable with `api-recon run`.

```bash
# First-time discovery (drives the REPL)
api-recon https://anikage.cc/api/media/anime/zMLNvt6MtV/episodes
# → REPL guides through probe, watch, click, capture
# → writes ~/.local/share/api-recon/recipes/anikage.cc.json

# Subsequent runs (no REPL)
api-recon run anikage.cc zMLNvt6MtV 1
# → looks up recipe, applies captured headers, downloads episode
```

The transcript of the REPL session becomes `examples/anikage.md`.

## Build order (implementation sequence)

1. `pkg/recipe/recipe.go` + `internal/store/store.go` — the store. Without
   it, captured state has nowhere to live.
2. `pkg/shape/shape.go` — the classifier. Everything else depends on it.
3. `pkg/creds/creds.go` — small, mechanical, high value.
4. `pkg/graph/graph.go` — uses classifier output to build sibling graph.
5. `pkg/fuzz/fuzz.go` — uses graph + classifier for informed attempts.
6. `pkg/download/download.go` — uses classifier + creds + recipe to
   download from any recognized source.
7. `internal/harvest/harvest.go` — drives the above into a guided flow
   that writes a recipe.
8. `pkg/action/action.go` + `internal/repl/*` — REPL wraps harvest.
9. `main.go` — wire REPL as default, keep subcommand dispatch.
10. Refactor existing subcommands to expose `Action` structs.
11. Rewrite `examples/anikage.md` as a real transcript.

## Why this is better than the old plan

The old PLAN.md built a friendlier flashlight. This plan builds a tool
that:

- **Remembers** what it found, so you don't rediscover it
- **Replays** the discovery + capture + download in one command
- **Adapts** to any source shape, not just hardcoded recipes
- **Heals** itself by recognizing when responses change (`verify`)
- **Teaches** the user by surfacing heuristics as suggestions

The REPL is the front door for new domains. The store is the daily-use
interface for known ones. The shape classifier is the brain that ties
them together.
