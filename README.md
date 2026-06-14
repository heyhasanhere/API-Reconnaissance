# api-recon

A stateful knowledge-mining CLI for discovering and replaying APIs.

Paste a URL. The tool watches traffic, classifies responses by shape,
captures auth, follows sibling endpoints, and writes a recipe you can
re-run on demand. See `examples/anikage.md` for the worked example.

## Install

`api-recon` is a Go module (`github.com/falcon/api-recon`, go 1.26.4,
no runtime deps). The `watch` and `click` REPL actions also need
Playwright; everything else works without it.

### Local install (you have the source)

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

After this, `api-recon --version` works from any directory. Re-run
`go install .` from the project root to pick up new commits.

### Clone + build (anyone else)

```bash
git clone <repo-url> api-recon
cd api-recon
go install .
```

The repo URL is whatever your `git remote -v` reports. The module
path in `go.mod` doesn't have to match the URL, but they do need
to match for `go install <url>@latest` to work without a clone.

### Playwright (only if you use `watch` or `click`)

```bash
npm install && npx playwright install chromium
```

`api-recon` prints a one-line install hint on REPL startup if
Playwright is missing. The basic probe/harvest/verify/run path
doesn't need it.

## Usage

```bash
api-recon [url]                        # enter REPL (auto-probe if url given)
api-recon harvest <url>                # guided discovery for a domain
api-recon run <domain> [args...]       # replay a saved recipe
api-recon ls                           # list known domains
api-recon show <domain>                # print a recipe
api-recon verify <domain>              # re-probe endpoints, report drift
api-recon recipe edit <domain>         # open in $EDITOR (or vi)
```

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

Global flags:

- `--json` — scriptable output for subcommands
- `--no-repl` — force subcommand mode even when a URL is given
- `--store <dir>` — override the recipe store location
- `--help`, `-h` — show help
- `--version` — print version

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
- `error` — 5xx with structured error envelope
- `redirect` — 3xx
- `unknown` — fallback

Each shape maps to a download strategy (yt-dlp, curl, aria2c, …) and
to a set of REPL suggestions. The **graph** records the parent/child
and sibling relationships between observed endpoints, which the REPL
uses to suggest "try a sibling under the same prefix." The **creds**
store tracks captured headers per host with the URL they were first
seen on, so the REPL can suggest "refresh the token via the captured
endpoint" on a 401.

## The REPL

The REPL is the default mode. Given a URL, it auto-probes and
emits a menu of next steps based on what it learned. Suggestions
are starred.

```
$ api-recon https://anikage.cc/api/media/anime/zMLNvt6MtV/episodes
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

pick a number, name, or 'q':
```

`help <action>` shows flags, examples, and what the action does.
`q` quits without saving.

## Subcommands

`api-recon` defaults to the REPL. Pass a subcommand to script it.

| Subcommand | What it does |
|------------|--------------|
| `harvest <url>` | Enter the REPL with a URL pre-loaded |
| `run <domain> [args...]` | Replay a saved recipe; positional args fill placeholders in order |
| `ls` | List known domains (project + global, deduplicated) |
| `show <domain>` | Print a recipe (or `--json` for the raw JSON) |
| `verify <domain>` | Re-probe every endpoint, report drift against the recorded shape |
| `recipe edit <domain>` | Open the recipe in `$EDITOR` (falls back to `vi`) |

`run` is the headline subcommand: one invocation replays the
discovery, fills the placeholders from the positional args, and
exercises every endpoint in the recipe.

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
