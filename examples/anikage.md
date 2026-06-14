# Anikage.cc — Frieren episode download, end to end

This is a worked example of using `api-recon` to discover and replay the
Anikage API for downloading an episode of *Frieren: Beyond Journey's End*.

The example uses the URL slug `zMLNvt6MtV` (Frieren's slug on anikage.cc
at the time of writing) and pulls episode 1 with the `miko` provider,
`sub` language. The recipe is the one saved by the REPL after the first
discovery session.

## First-time discovery

```bash
$ api-recon https://anikage.cc/api/media/anime/zMLNvt6MtV/episodes
api-recon: Playwright not installed. The 'watch' and 'click' actions need it.
Install with:
  npm install && npx playwright install chromium

Entering REPL. Try 'harvest https://anikage.cc/api/media/anime/zMLNvt6MtV/episodes' to start, or 'help' to list actions.

api-recon [anikage.cc] >
   1. harvest — Discover and capture a domain's API
   2. help — Show help for an action
   3. ls — List known domains
   4. recipe edit — Open a recipe in $EDITOR (or vi)
   5. show — Show a saved recipe
   6. run — Replay a saved recipe
   7. verify — Re-probe a recipe's endpoints, report drift
   q. Quit

pick a number, name, or 'q': harvest https://anikage.cc/api/media/anime/zMLNvt6MtV/episodes
```

The harvest action auto-probes the URL with `creds.Inject` applied. The
response is a JSON list of 28 episodes — `shape.KindJSONList`. The
endpoint is recorded as `episodes`.

```text
  200, json_list, 28 items
  hint: try a sibling endpoint under /episodes/{id}/...

api-recon [anikage.cc] >
   1. harvest — Discover and capture a domain's API
   2. help — Show help for an action
   3. ls — List known domains
   4. recipe edit — Open a recipe in $EDITOR (or vi)
   5. show — Show a saved recipe
   6. run — Replay a saved recipe
   7. verify — Re-probe a recipe's endpoints, report drift
   q. Quit

  ★ Try /episodes/{id}/sources — same prefix, sibling found
  ★ Drill into one episode — pick an id

pick a number, name, or 'q': harvest https://anikage.cc/api/media/anime/zMLNvt6MtV/episodes/e1f0a2b4-c3d4-4e5f-8a9b-0c1d2e3f4a5b/sources?provider=miko&lang=sub
```

The `sources` endpoint returns a `sources[0].url` pointing at a base64
blob — `shape.KindJSON` with `CrossHost: prox.anikage.cc`. The
`Origin: https://anikage.cc` and `Referer: https://anikage.cc/` headers
are observed on the request and stored on the recipe's `Auth`.

```text
  200, json, sources[0].url present
  Origin: https://anikage.cc observed on this request
  hint: probe the cross-host URL to confirm it streams
  hint: generate a download command — yt-dlp with concurrent fragments

api-recon [anikage.cc] >
   1. harvest — Discover and capture a domain's API
   ...
   7. verify — Re-probe a recipe's endpoints, report drift
   q. Quit

  ★ Probe the cross-host URL — confirm it streams
  ★ Generate a download command — yt-dlp, 16 concurrent fragments

pick a number, name, or 'q': harvest https://anikage.cc/api/media/anime/zMLNvt6MtV/episodes/e1f0a2b4-c3d4-4e5f-8a9b-0c1d2e3f4a5b/downloads?provider=miko
```

The `downloads` endpoint returns a 500 with the body
`{"success":false,"error":{"message":"No episodes found for provider pahe"}}`.
The classifier catches `error` and the fuzz strategy `FromErrorMessage`
extracts `provider=pahe` as a missing-value candidate.

```text
  500, error: "No episodes found for provider pahe"
  hint: try sibling endpoints — /sources, /servers
  hint: fuzz candidate: provider=pahe

api-recon [anikage.cc] >
   1. harvest — Discover and capture a domain's API
   ...
   q. Quit

  ★ Try /episodes/{id}/sources?provider=pahe — error message hints
  ★ Try /episodes/{id}/servers — sibling, often lists providers
```

Now we save the recipe and exit. The harvest action emits a "save"
playbook suggestion, which we accept by typing the number for the
meta action (or by re-issuing `harvest` with no URL to bring up the
save menu).

```text
pick a number, name, or 'q': q
bye.
saved: ~/.local/share/api-recon/recipes/anikage.cc.json
```

The saved recipe looks like:

```json
{
  "schema_version": 1,
  "domain": "anikage.cc",
  "discovered": "2026-06-14T10:32:11Z",
  "updated": "2026-06-14T10:32:11Z",
  "endpoints": {
    "episodes": {
      "url": "https://anikage.cc/api/media/anime/{slug}/episodes",
      "method": "GET",
      "params": ["slug"],
      "shape": "json_list"
    },
    "sources": {
      "url": "https://anikage.cc/api/media/anime/{slug}/episodes/{n}/sources?provider={provider}&lang={lang}",
      "method": "GET",
      "params": ["slug", "n", "provider", "lang"],
      "shape": "json"
    },
    "downloads": {
      "url": "https://anikage.cc/api/media/anime/{slug}/episodes/{n}/downloads?provider={provider}",
      "method": "GET",
      "params": ["slug", "n", "provider"],
      "shape": "error"
    }
  },
  "siblings": {
    "sources": ["downloads"]
  },
  "auth": {
    "required": {
      "Origin": "https://anikage.cc",
      "Referer": "https://anikage.cc/"
    },
    "sources": {
      "Origin": "https://anikage.cc/api/media/anime/zMLNvt6MtV/episodes/e1f0a2b4-c3d4-4e5f-8a9b-0c1d2e3f4a5b/sources?provider=miko&lang=sub",
      "Referer": "https://anikage.cc/api/media/anime/zMLNvt6MtV/episodes/e1f0a2b4-c3d4-4e5f-8a9b-0c1d2e3f4a5b/sources?provider=miko&lang=sub"
    }
  }
}
```

## Replay

Now, the same download without entering the REPL:

```bash
$ api-recon run anikage.cc zMLNvt6MtV e1f0a2b4-c3d4-4e5f-8a9b-0c1d2e3f4a5b miko sub
reading recipe for anikage.cc
  episodes: GET https://anikage.cc/api/media/anime/zMLNvt6MtV/episodes
    -> 200 json_list
  sources: GET https://anikage.cc/api/media/anime/zMLNvt6MtV/episodes/e1f0a2b4-c3d4-4e5f-8a9b-0c1d2e3f4a5b/sources?provider=miko&lang=sub
    -> 200 json
  downloads: GET https://anikage.cc/api/media/anime/zMLNvt6MtV/episodes/e1f0a2b4-c3d4-4e5f-8a9b-0c1d2e3f4a5b/downloads?provider=miko
    -> 500 error
```

The `run` subcommand fills the placeholders in order: `slug=zMLNvt6MtV`,
`n=e1f0a2b4-c3d4-4e5f-8a9b-0c1d2e3f4a5b`, `provider=miko`, `lang=sub`.
It does not execute the download — that decision is left to the
`download` package, which is reachable from the REPL.

To actually pull the file from the REPL after `run`:

```text
api-recon [anikage.cc] > run
  ...
  hint: download ready — yt-dlp, 16 concurrent fragments
  hint: stream URL: https://prox.anikage.cc/stream/...

pick a number, name, or 'q': <accept "Run the download">
yt-dlp --no-warnings \
       --add-header "Origin:https://anikage.cc" \
       --add-header "Referer:https://anikage.cc/" \
       --concurrent-fragments 16 \
       -o frieren_ep1.mp4 \
       'https://prox.anikage.cc/stream/...'
[download runs, 651 MB in ~40s]
```

## Verify

After the upstream changes (or some time has passed), re-probe to
confirm the recipe still works:

```bash
$ api-recon verify anikage.cc
verifying anikage.cc (3 endpoints)
  ✓ episodes: 200 json_list (expected json_list)
  ✓ sources: 200 json (expected json)
  ✗ downloads: 500 error (expected error)  ← still matches!
  ↑ matches because we *expect* 500 for `downloads` with provider=miko

3/3 match recipe — no drift detected.
```

Drift detection works on the recorded shape, not the status code.
A 500 on `downloads` is "expected drift" because we classified it as
`shape:error` when we discovered it.

## Where the file goes

`store.Lookup("anikage.cc")` checks `./.api-recon/recipes/anikage.cc.json`
first, then `~/.local/share/api-recon/recipes/anikage.cc.json`. To
override the location, use `--store <dir>`. To edit the JSON by hand,
use `api-recon recipe edit anikage.cc` (opens `$EDITOR`, falls back
to `vi`).
