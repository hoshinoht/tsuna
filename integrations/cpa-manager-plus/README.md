# Optional CPA-Manager-Plus integration

CPA-Manager-Plus Full Mode adds persistent request history, usage and estimated
cost analytics alongside Tsuna's local CPA v8 gateway. The overlay pins CPAMP to
v1.14.6 and publishes the portal only on localhost port 18318. Model clients keep
using port 18317.

From the Tsuna checkout:

```sh
tsuna proxy init
bun run integration:cpa init
bun run integration:cpa start
```

Start recreates the gateway with authenticated management and usage collection
enabled, briefly interrupting requests. Open
[the portal](http://127.0.0.1:18318/management.html) and log in with the contents of
`.runtime/cpa-manager-plus/admin-key`. Read that file locally; it is not printed
by the integration commands. The upstream URL and management key are provided
through the private Docker network and a Compose secret, so no manual CPA
connection setup is needed. Keep Monitoring enabled in the portal.

```sh
bun run integration:cpa status
bun run integration:cpa stop
```

Stop stops only Manager Server and recreates the gateway using its original
configuration. It keeps history and credentials. Start selects the overlay in
`config/proxy.json`, so later `tsuna proxy start` and OAuth logins use the same
gateway configuration. Stop removes that selection. `tsuna proxy stop` stops
the gateway only; use the integration's stop command to disable both tracking
and management access. The default Compose file never starts Manager Server.

## Configuration and data

Initialization creates private files under the Git-ignored
`.runtime/cpa-manager-plus/` directory:

| Path | Purpose |
| --- | --- |
| `gateway.yaml` | Copy of the base gateway config, with management and usage enabled |
| `management-key` | Gateway management credential |
| `admin-key` | Portal login credential |
| `data/` | SQLite history, configuration, archives and encryption key |

Repeated initialization preserves these files. The base gateway config and
OAuth directory are retained. Manager edits to gateway settings are saved to
the integration's writable `gateway.yaml`; they apply while the integration is
enabled. To retain such edits in the default gateway, deliberately apply them
to `.runtime/proxy/config.yaml` too. Do not copy management or usage settings
back unless you want them enabled without this integration.

Back up the entire integration directory while Manager Server is stopped,
including `data/data.key`, SQLite sidecars, archives and both credentials. Stop
does not delete data. Tracking begins with newly collected traffic and cannot
recover expired gateway events. Run one usage collector against this gateway.
Cost figures are estimates based on configured model prices.

## External gateways

The helper accepts only this checkout's `compose.yml` / `gateway` target and
refuses custom or Kanata targets. For an external gateway, deploy a separate
CPAMP service following the upstream Docker guide, supply its reachable CPA URL
and management key, and enable usage publishing in that gateway's v8 config.
Do not run a second gateway on Tsuna's port 18317.

## References

- [CPAMP Docker setup and secret-file settings](https://seakee.github.io/CPA-Manager-Plus/docs/en/deployment/docker.html)
- [Pinned release v1.14.6](https://github.com/seakee/CPA-Manager-Plus/releases/tag/v1.14.6)
- [Monitoring troubleshooting](https://seakee.github.io/CPA-Manager-Plus/docs/en/troubleshooting/request-monitoring.html)
