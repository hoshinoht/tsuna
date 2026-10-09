# gofetch-mcp

A minimal Go MCP (stdio) server for agent web access. Two tools, nothing else:

| Tool | Purpose |
|------|---------|
| `fetch` | Fetch a URL (HTML, PDF, plain text or Markdown) as readable markdown, with an explicit extraction-quality verdict. `focus` narrows output to matching sections; `max_chars`/`offset`/`document_id` paginate over one cached extraction. |
| `web_search` | Web search via the Exa API when a key is configured, falling back to keyless DuckDuckGo/Mojeek scraping. Rate-limited providers are retried (honouring `Retry-After`) within a total deadline, then the next provider takes over. Reports which provider answered (`source`) and what happened at each one (`attempts`). |

Built to replace kitchen-sink fetch/search MCP servers with a single static
binary: no cold-start dependency downloads, no upstream tool renames, and a
tool surface that only changes when you change it.

## Build

```sh
make build   # -> bin/gofetch
make test    # go test ./...
go test -race ./...
```

## `fetch`

Input: `url` (required), `focus`, `max_chars` (default 40000, max 150000),
`offset`, `document_id`.

```json
{"url": "https://go.dev/doc/effective_go", "focus": "\"named results\" defer", "max_chars": 20000}
```

Result fields worth knowing:

- `quality`: `usable`, `thin` (under 200 characters), `blocked` (an HTTP 200
  challenge, sign-in or consent page) or `empty` (no text, or a JavaScript
  shell). `quality_reason` narrows it (`challenge`, `login_required`,
  `consent_wall`, `javascript_required`, `no_text`, `short_content`) and
  `warnings` explains it in a sentence.
- `content_ok`: kept for compatibility; true exactly when `quality` is
  `usable`. It is an **extraction-quality signal, not a check that the
  content is accurate, current or complete**.
- `kind`: `html`, `pdf`, `text` or `markdown`. Other formats (images,
  archives, office documents) fail with `unsupported_format` rather than
  being parsed as HTML.
- `focus_status`: `matched` or `no_match` (the full document is returned,
  with a warning). Quote phrases in `focus` to match them exactly; common
  stopwords are ignored, and the matched section's heading is kept.
- `total_chars`, `offset`, `next_offset`: Unicode code points, not bytes.
  Pages end on a Markdown block boundary where one is close.
- `document_id`: a hash of the extraction. Pass it back with `next_offset`:
  later pages come from the same cached extraction, and if the page changed
  the call fails with `document_changed` (carrying the new `document_id`)
  instead of mixing versions. `document_expired` means the extraction left
  the cache and could not be re-fetched.

Extractions are cached for 10 minutes (64 entries / 64 MiB, LRU);
concurrent requests for the same URL share one download.

## `web_search`

Input: `query` (required), `num_results` (default 5, max 10).

On failure `error.code` distinguishes `no_results` (a provider answered with
nothing) from `rate_limited`, `auth_failed`, `quota_exceeded`, `blocked`,
`timeout`, `parse_error`, `canceled` and `search_unavailable` (mixed
failures). `attempts` lists each provider's outcome, HTTP status, tries,
`retry_after_s` and elapsed time. Credentials never appear in results or
errors.

## Configuration

| Setting | Effect |
|---------|--------|
| `EXA_API_KEY` | Exa API key (highest precedence). |
| `-exa-key-file PATH` / `GOFETCH_EXA_KEY_FILE=PATH` | Read the key from a file (`~/` is expanded). The flag wins over the variable. `none` disables key files. |
| *(legacy)* `~/.config/opencode/.exa-api-key` | Still read when nothing above is set, with a notice on stderr. Set `GOFETCH_EXA_KEY_FILE` to make the choice explicit. |

Without a key, search uses DuckDuckGo and Mojeek only.

## Client configuration

### Standalone (any MCP client)

```sh
EXA_API_KEY=... /path/to/gofetch            # speaks MCP on stdin/stdout
/path/to/gofetch -exa-key-file ~/.secrets/exa
claude mcp add gofetch -- /path/to/gofetch -exa-key-file ~/.secrets/exa
```

Generic `mcpServers` JSON (Claude Desktop, Cursor, ...):

```json
{
  "mcpServers": {
    "gofetch": {
      "command": "/path/to/gofetch",
      "args": ["-exa-key-file", "/home/you/.secrets/exa"]
    }
  }
}
```

### Hoshi

Hoshi reads oh-my-pi style `mcp.json` (`mcpServers` map; `${VAR}`
placeholders are expanded; an `env` value naming a set environment variable
passes that variable through):

```json
{
  "mcpServers": {
    "gofetch": {
      "type": "stdio",
      "command": "${HOME}/.config/hoshi-omp/mcps/gofetch-mcp/bin/gofetch",
      "env": {
        "GOFETCH_EXA_KEY_FILE": "${HOME}/.config/hoshi-omp/.exa-api-key"
      }
    }
  }
}
```

### OpenCode

```json
"gofetch": {
  "type": "local",
  "command": ["sh", "-c", "$HOME/.config/opencode/mcps/gofetch-mcp/bin/gofetch -exa-key-file $HOME/.config/opencode/.exa-api-key"],
  "enabled": true
}
```

Agents then see `gofetch_fetch` and `gofetch_web_search`.
