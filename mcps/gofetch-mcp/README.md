# gofetch-mcp

A minimal Go MCP (stdio) server for agent web access. Two tools, nothing else:

| Tool | Purpose |
|------|---------|
| `fetch` | Fetch a URL (HTML or PDF) as readable markdown. `focus` narrows output to matching sections; `max_chars`/`offset` paginate. Check `content_ok` before trusting content. |
| `web_search` | Web search via the Exa API when a key is available (`EXA_API_KEY` env or `~/.config/opencode/.exa-api-key`), falling back to keyless DuckDuckGo/Mojeek scraping. A rate-limited (HTTP 429) backend is retried once after a short backoff, then the next backend takes over. Reports which backend answered via `source`. |

Built to replace kitchen-sink fetch/search MCP servers with a single static
binary: no cold-start dependency downloads, no upstream tool renames, and a
tool surface that only changes when you change it.

## Build

```sh
make build   # -> bin/gofetch
make test
```

## OpenCode config

```json
"gofetch": {
  "type": "local",
  "command": ["sh", "-c", "$HOME/.config/opencode/mcps/gofetch-mcp/bin/gofetch"],
  "enabled": true
}
```

Agents then see `gofetch_fetch` and `gofetch_web_search`.
