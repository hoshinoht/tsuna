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
