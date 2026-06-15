# api-recon

A stateful knowledge-mining CLI for discovering and replaying APIs.

Paste a URL. The tool watches traffic, classifies responses by shape,
captures auth, follows sibling endpoints, and writes a recipe you can
re-run on demand. See `examples/anikage.md` for the worked example.

## Install

`api-recon` is a Go module
(`github.com/heyhasanhere/API-Reconnaissance`, go 1.26.4, no
runtime deps). The `watch` and `click` REPL actions also need
Playwright; everything else works without it.

Pick whichever install path suits you — both produce the same
binary.

### Direct install (no clone)

Once the repo is public on GitHub, you can install with
`go install` directly. The Go module proxy caches `@latest`
based on the default branch's HEAD, so it can be stale for
hours after a push. **Pin to a version (recommended), or
use `@main` for the latest commit.**

```bash
# Recommended — pinned to a tagged version, always reproducible
go install github.com/heyhasanhere/API-Reconnaissance@v0.1.0

# Latest tagged release (catches up to v0.1.0 once the proxy reindexes)
go install github.com/heyhasanhere/API-Reconnaissance@latest

# Latest commit on the default branch (always current, may be unstable)
go install github.com/heyhasanhere/API-Reconnaissance@main
```

To cut a new tag for your own use:

```bash
git tag v0.1.0
git push origin v0.1.0
go install github.com/heyhasanhere/API-Reconnaissance@v0.1.0
```

### Local install (clone + build)

Use this path if you want to read the source, modify the code,
or work on `main` directly without waiting for a tagged release.

```bash
git clone https://github.com/heyhasanhere/API-Reconnaissance.git
cd API-Reconnaissance
go install .
```

Or, if you already have the source somewhere on disk
(not necessarily under your `$GOPATH`):

```bash
cd /path/to/api-recon
go install .
```

This places the `api-recon` binary in `$GOBIN` (default
`~/go/bin/`). Add that to your `PATH` once:

```bash
echo 'export PATH="$HOME/go/bin:$PATH"' >> ~/.zshrc
source ~/.zshrc
```

After this, `api-recon --version` works from any directory.
Re-run `go install .` from the project root to pick up new
commits.

### Verifying the install

```bash
$ api-recon --version
0.1.0
```

If you get "command not found," `$HOME/go/bin` isn't on your
`PATH` — see the export line above.

### Playwright (only if you use `watch` or `click`)

The `watch` and `click` REPL actions spawn a headless Chromium
via Playwright. This is **only needed in the `api-recon`
project directory** — that's where the `package.json` lives
that declares Playwright as a dev dependency. The install
is one-time, not per-recipe.

```bash
cd /path/to/api-recon
npm install
npx playwright install chromium
```

Running `npm install` in any other directory will fail with
`ENOENT: no such file or directory, open '.../package.json'`
— that's npm saying "I don't know what to install here."

If you cloned via `go install` (no local source), Playwright
isn't relevant — only the project source tree uses it. The
`api-recon` REPL prints a one-line install hint on startup
if Playwright is missing. The basic probe/harvest/verify/run
path doesn't need it.

## Usage

```bash
api-recon --version          # 0.1.0
api-recon --help             # full usage
api-recon [url]              # enter REPL (auto-probe if url given)
api-recon ls                 # list known domains
api-recon show <domain>      # print a recipe
api-recon run <domain> ...   # replay a saved recipe
api-recon verify <domain>    # re-probe endpoints, report drift
api-recon recipe edit <d>    # open in $EDITOR (or vi)
```

Global flags:

- `--json` — scriptable output for subcommands
- `--no-repl` — force subcommand mode even when a URL is given
- `--store <dir>` — override the recipe store location
- `--help`, `-h` — show help
- `--version` — print version

### Where recipes go

By default, recipes are written to two places, in lookup order:

1. `./.api-recon/recipes/` — project-local, if the current
   directory is writable.
2. `~/.local/share/api-recon/recipes/` — XDG global (or
   `$XDG_DATA_HOME/api-recon/recipes/` if set).

`api-recon --store <dir>` overrides the writer. Useful when
you're working outside a project directory:

```bash
api-recon --store ~/notes/anime/.api-recon harvest <url>
api-recon --store ~/notes/anime/.api-recon ls
api-recon --store ~/notes/anime/.api-recon run anikage.cc
```

Files are mode 0600 (recipes may contain captured auth tokens).
Writes are atomic — a failed write leaves the previous recipe
intact, never a half-written file.

## Walkthrough: the anikage case

This is the canonical flow. Outputs below are real, captured from
the binary.

### 1. Enter the REPL with a URL

```bash
$ api-recon https://anikage.cc/api/media/anime/zMLNvt6MtV/episodes
Entering REPL. Auto-probing https://anikage.cc/api/media/anime/zMLNvt6MtV/episodes ...
probed https://anikage.cc/api/media/anime/zMLNvt6MtV/episodes → 200 (json_list, 12279 bytes)
  hint: list of 28 items
  hint: list with id field "id" — try drilling into one item

api-recon [anikage.cc] >
   1. harvest — Discover and capture a domain's API
   2. help — Show help for an action
   3. ls — List known domains
   4. recipe edit — Open a recipe in $EDITOR (or vi)
   5. save — Save the in-memory recipe to disk
   6. show — Show a recipe (in-memory if not yet saved)
   7. run — Replay a saved recipe
   8. verify — Re-probe a recipe's endpoints, report drift
   q. Quit

pick a number, name, or 'q':
```

The tool auto-probed the URL once. The shape classifier saw a
top-level JSON array and named it `json_list` (28 items, `id`
field detected). The `anikage.cc` host is now the in-flight
recipe's domain — that's what `[anikage.cc]` in the prompt
shows.

### 2. Drill into a child endpoint

```bash
pick a number, name, or 'q': harvest https://anikage.cc/api/media/anime/zMLNvt6MtV/episodes/019e6dd0-9af6-7fdb-82ce-14bda50833ee/sources?provider=miko&lang=sub
  probed https://anikage.cc/api/media/anime/zMLNvt6MtV/episodes/019e6dd0-9af6-7fdb-82ce-14bda50833ee/sources?provider=miko&lang=sub → 404 (error, 23 bytes)
  hint: error: Not found

api-recon [anikage.cc] >
   1. harvest — Discover and capture a domain's API
   2. help — Show help for an action
   ...

Next suggestions:
  extract values from the error and try them
  error response — likely a value or path mismatch
  5xx — boundary fuzzing might find a working value

pick a number, name, or 'q':
```

The `harvest` action takes a URL after it. This URL returned 404
with `{"message":"Not found"}`. The classifier caught the 4xx
and the JSON envelope, and put the message into a hint. The
shape was recorded as `error` (this is a real failure of the
live anikage site — the example historically used a `miko`
provider that no longer returns a 200).

### 3. Inspect the in-memory recipe

```bash
pick a number, name, or 'q': show
# anikage.cc (in-memory, not yet saved)
  discovered: 2026-06-15
  endpoints: 2
  episodes: GET https://anikage.cc/api/media/anime/zMLNvt6MtV/episodes (json_list)
  sources:  GET https://anikage.cc/api/media/anime/zMLNvt6MtV/episodes/019e6dd0-9af6-7fdb-82ce-14bda50833ee/sources?provider=miko&lang=sub (error)

  showed anikage.cc (in-memory)
```

Two endpoints recorded so far. `show` with no arg shows the
in-memory recipe; with `<domain>` it falls back to disk. Each
endpoint is named from the last URL path segment, has its
discovered method, and its classified shape.

### 4. Save and exit

```bash
pick a number, name, or 'q': save
  saved recipe for anikage.cc to /tmp/empty-demo/anikage.cc.json

pick a number, name, or 'q': q
bye.
```

The save is atomic (write to `*.tmp` with mode 0600, fsync,
rename) and the file is human-readable JSON.

### 5. Use the subcommands from the shell

#### `ls`

```bash
$ api-recon ls
anikage.cc
```

With `--json`, output is one domain per line (scriptable).

#### `show <domain>`

```bash
$ api-recon show anikage.cc
# anikage.cc
discovered: 2026-06-15
endpoints: 2
  episodes: GET https://anikage.cc/api/media/anime/zMLNvt6MtV/episodes (json_list)
  sources: GET https://anikage.cc/api/media/anime/zMLNvt6MtV/episodes/019e6dd0-9af6-7fdb-82ce-14bda50833ee/sources?provider=miko&lang=sub (error)
```

#### `show --json <domain>`

```bash
$ api-recon --json show anikage.cc
{
  "schema_version": 1,
  "domain": "anikage.cc",
  "discovered": "2026-06-15T00:33:43.546768Z",
  "updated": "2026-06-15T00:33:44.297442Z",
  "endpoints": {
    "episodes": {
      "url": "https://anikage.cc/api/media/anime/zMLNvt6MtV/episodes",
      "method": "GET",
      "shape": "json_list"
    },
    "sources": {
      "url": "https://anikage.cc/api/media/anime/zMLNvt6MtV/episodes/019e6dd0-9af6-7fdb-82ce-14bda50833ee/sources?provider=miko\u0026lang=sub",
      "method": "GET",
      "params": [
        "provider",
        "lang"
      ],
      "shape": "error"
    }
  },
  "auth": {
    "required_headers": {
      "User-Agent": "api-recon/0.1.0"
    }
  }
}
```

The recipe is the on-disk artifact. Edit it by hand with
`api-recon recipe edit anikage.cc`.

#### `verify <domain>`

```bash
$ api-recon verify anikage.cc
verifying anikage.cc (2 endpoints)
  ✓ episodes: 200 json_list (expected json_list)
  ✓ sources: 404 error (expected error)

2/2 match recipe — no drift detected.
```

Re-probes every endpoint in the recipe and compares the
recorded shape. For `error`-shape endpoints, a 4xx/5xx is the
*expected* response and doesn't count as drift. For other
shapes, anything outside 2xx-3xx counts as drift.

#### `run <domain>`

```bash
$ api-recon run anikage.cc
  episodes: GET https://anikage.cc/api/media/anime/zMLNvt6MtV/episodes
    -> 200 json_list
  sources: GET https://anikage.cc/api/media/anime/zMLNvt6MtV/episodes/019e6dd0-9af6-7fdb-82ce-14bda50833ee/sources?provider=miko&lang=sub
    -> 404 error
```

Probes every endpoint. Positional args after `<domain>` fill the
**main endpoint's** placeholders in order (the main endpoint is
`"sources"` if present, else the first endpoint with
placeholders, else the first endpoint overall). The same
parts map is then applied to every endpoint — endpoints with
placeholders the args don't cover are reported as
`skip (no parts: ...)` because `recipe.Fill` is strict.
The `run` path leaves the user with a populated `graph.Graph`
alongside the printed output.

#### `recipe edit <domain>`

```bash
$ EDITOR=vim api-recon recipe edit anikage.cc
# opens $EDITOR (or vi) on the saved recipe JSON
```

Falls back to `vi` if `$EDITOR` is unset. Edits are saved
back to the same atomic-write path the REPL uses.

## How it works

A **recipe** is a JSON file that records what we learned about a
domain: the entry endpoint, sibling endpoints under the same prefix,
captured `Origin`/`Referer`/`Authorization` headers with the URL each
was first seen on, and a shape classification per endpoint.

Recipes are stored under `~/.local/share/api-recon/recipes/` (XDG-aware)
or `./.api-recon/recipes/` for a project-local override. Lookups
check project first, then global. Writes are atomic; files are mode
0600 because they may contain captured tokens.

The **shape classifier** is the brain. It looks at the response body
and content-type and picks one of:

- `json_list` — top-level JSON array (with id-field extraction)
- `json` — JSON object (with cross-host URL detection)
- `hls_master` / `hls_variant` — Apple HLS playlists
- `dash` — MPEG-DASH manifest
- `direct` — known video extension with a large content-length
- `segment_list` — JSON array of URL strings
- `html` / `form` — HTML pages, with form-action detection
- `error` — 4xx/5xx with structured error envelope
- `redirect` — 3xx
- `unknown` — fallback

Each shape maps to a download strategy (yt-dlp, curl, aria2c, …) and
to a set of REPL suggestions. The **graph** records the parent/child
and sibling relationships between observed endpoints, which the REPL
uses to suggest "try a sibling under the same prefix." The **creds**
store tracks captured headers per host with the URL they were first
seen on, so the REPL can suggest "refresh the token via the captured
endpoint" on a 401.

## Design notes

- **`recipe.Fill` is strict.** It errors on missing or unknown
  placeholders. Recipes do not silently fail to a wrong URL.
- **Downloads are opt-in.** `download.Plan` is shown to the user
  for inspection. The REPL offers "run the download" as a separate
  choice; it never auto-executes.
- **Action is a struct, not an interface.** Metadata is a literal;
  the runner is a closure that captures dependencies. This makes
  help text, examples, and category metadata cheap to render.
- **No flag explosion.** Global flags are `--json`, `--no-repl`,
  `--store`, `--help`, `--version`. Per-action flags exist on the
  action's metadata, not as top-level flags.
- **Module path matches the GitHub URL.** The `go.mod` module
  is `github.com/heyhasanhere/API-Reconnaissance` so
  `go install github.com/heyhasanhere/API-Reconnaissance@v0.1.0`
  resolves once the repo is public.

See `.context/PLAN.md` for the full design and `.context/LOG.md` for
the chronological log of the anikage investigation that seeded the
shape, fuzz, and download strategies.

## Project layout

```
api-recon/
├── main.go                # dispatcher (REPL vs subcommand)
├── pkg/
│   ├── action/            # Action struct, Result, Context
│   ├── recipe/            # Recipe type, Fill (strict), JSON codec
│   ├── shape/             # response shape classifier
│   ├── creds/             # credential capture + injection
│   ├── graph/             # endpoint graph (siblings, parents, ids)
│   ├── fuzz/              # informed fuzzing strategies
│   └── download/          # download interpreter (yt-dlp, curl, aria2c)
├── internal/
│   ├── cli/               # argv parsing helper (Split)
│   ├── harvest/           # guided discovery driver (REPL action)
│   ├── repl/              # REPL loop, menu, suggest, help, playbooks
│   ├── store/             # file-based recipe store
│   ├── capture/           # Go side: invokes the Playwright helper
│   └── node/              # Playwright helper (helper.mjs)
├── testdata/              # canned HTTP exchanges for tests
├── examples/anikage.md    # worked REPL transcript
└── .context/              # design doc (PLAN.md) + log (LOG.md)
```

## License

Personal project. Treat the recipe files as sensitive — they may
contain captured auth tokens.
