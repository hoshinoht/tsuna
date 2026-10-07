# Trial validation — 2026-10-07

Validated with OMP 18.8.0 on macOS arm64. No model completion request was sent.

- TypeScript check and focused adapter/policy tests passed.
- The document port passed real Pandoc, XeLaTeX, pdfLaTeX, BibTeX and conversion integration tests.
- The actual OMP SDK loaded the compatibility extensions and all 17 agents. Its wrapped read tool allowed a project file and rejected an external-directory read requiring approval in a headless session.
- OMP MCP startup discovered Shiori's 13 tools, gofetch's two tools, researcher's ten tools and LSP's seven tools. Shiori `workplan_list` returned the temporary project's root.
- Shiori was rebuilt from its vendored source.
- Interactive startup reached the main prompt with all five enabled MCP servers connected, including Context7. The temporary test terminal was stopped afterwards.
- CLIProxyAPI's authenticated model-list health check passed. Docker publishes only `127.0.0.1:18317`; no provider accounts were signed in during setup.
- Independent review found and then verified fixes for URI permission routing and missing fresh-install builds.

## Memory sample

| Sample | Observed |
|---|---:|
| OMP process, fresh idle prompt | 402.1 MiB RSS |
| OMP, launcher, four local MCP processes and terminal shell | 484.8 MiB summed RSS |
| CLIProxyAPI container, idle | 77.85 MiB Docker memory report |
| Existing OpenCode processes | 359.1 + 166.5 MiB RSS, excluding MCPs |

These are local observations, not a controlled benchmark. The OpenCode processes had a different existing workload. Summed RSS may include shared pages; Docker's memory report uses a different accounting method and excludes the VM's overhead. Language servers, browsers, model activity and concurrent workers were not exercised in this memory sample.

## Still requires provider sign-in

Authenticate Claude and Codex through `hoshi-omp proxy login`, then list models and test an ordinary turn. Live model availability, reasoning settings, extended-context limits, prompt-cache behavior and subscription quota endpoints remain unverified until those accounts are available.
