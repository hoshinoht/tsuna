# CLIProxyAPI gateway

`tsuna proxy init` creates the local API key and gateway configuration in
`.runtime/proxy/`. The directory is ignored by Git. The gateway publishes its
API only at `http://127.0.0.1:18317`; OMP reads that endpoint through its
configured `cliproxy-openai` and `cliproxy-anthropic` providers.

Use the wrapper through the PATH launcher:

```sh
tsuna proxy init
tsuna proxy start
tsuna proxy status
tsuna proxy models
tsuna proxy login claude
tsuna proxy login codex
tsuna proxy stop
```

`login` starts a temporary interactive container and publishes only the needed
OAuth callback on localhost: port 54545 for Claude or port 1455 for Codex.
Credentials persist in `.runtime/proxy/auth/`; the normal gateway container
uses the same directory. The temporary command explicitly runs the image's
`./CLIProxyAPI` executable with `--no-browser`; open the URL it prints in your
host browser. `start` waits up to 30 seconds for an authenticated health check.
`models` and `status` make an authenticated `GET /v1/models` request and do not
invoke a model.

The Compose service is pinned to CLIProxyAPI v8.0.18 by image digest. Its
generated configuration binds the service inside the container on `0.0.0.0`,
publishes only the loopback host port, disables remote management and the
control panel, turns off request logging, and enables session affinity.

## Compose target

`config/proxy.json` names the Compose service that `start`, `stop` and `login`
drive. The tracked default is this checkout's `compose.yml` and its `gateway`
service:

```json
{ "projectDirectory": ".", "composeFiles": ["compose.yml"], "service": "gateway" }
```

`projectDirectory` is relative to the checkout or starts with `~/`; each
`composeFiles` entry is relative to `projectDirectory` or starts with `~/`.
Tsuna passes them as `docker compose --project-directory DIR -f FILE...`, so
Compose reads that directory's `.env` (project name and interpolated paths).

To let Kanata own the container, point the setting at Kanata's checkout and its
`cliproxy` service (see Kanata's `deploy/docker/README.md`, "CLIProxyAPI
upstream"), for example:

```json
{
  "projectDirectory": "~/projects/personal/homelab/kanata",
  "composeFiles": ["compose.kanata.yml", "compose.kanata.cliproxy.yml"],
  "service": "cliproxy"
}
```

Kanata's `.env` must set `CLIPROXY_CONFIG_FILE` and `CLIPROXY_AUTH_DIR` to this
checkout's `.runtime/proxy/config.yaml` and `.runtime/proxy/auth`, so the client
key, configuration and sign-ins stay the ones `init` created here. `start` then
runs `up -d cliproxy`, which starts only that service and leaves Kanata's own
containers as they are; `login` runs the same `run --rm --no-deps -p` callback
container against the Kanata service, and `status`/`models` keep using
`http://127.0.0.1:18317`. Stop `tsuna-gateway` first (Kanata's cutover
runbook): both publish host port 18317.
