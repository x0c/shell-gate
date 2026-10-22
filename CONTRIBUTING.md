# Contributing

Read [AGENTS.md](AGENTS.md) and the relevant design documents first. Keep changes focused and preserve terminal, mobile keyboard, account isolation and persistent-tab behavior.

Use Go 1.26.10 or newer. Run `go test -race ./...`, `go vet ./...`, `golangci-lint run ./...` and `govulncheck ./...`. For browser logic, run `node --test internal/server/web/login_test.mjs internal/server/web/security_test.mjs internal/server/web/enhance_zoom_test.mjs internal/server/web/enhance_query_frame_test.mjs` with Node 22 or newer. Keep full diagnostics and explain any retained finding. Run the real affected path; terminal changes require a real ttyd WebSocket handshake and input/output check.

Do not add private hosts, personal addresses, credentials, database files or screenshots containing personal data. Run `python3 scripts/check-public.py` before publishing. Report vulnerabilities privately using GitHub's private vulnerability reporting when enabled; otherwise contact the maintainer through the profile linked by this repository.

Build packages locally with `python3 scripts/release.py --version VERSION`. This does not depend on CI. ShellGate code is MIT; dependencies retain their own licenses.

CI runner assessment: GitHub emits an infrastructure notice that `ubuntu-latest` is migrating from Ubuntu 24.04 to 26.04 during October–November 2026 ([official announcement](https://github.com/actions/runner-images/issues/14748)). Retain the rolling matrix to exercise current supported hosts; release packages are built locally with pinned Go and `CGO_ENABLED=0`. This is not a compiler/test failure. Residual risk is runner-provided tool or OS behavior changing during rollout; inspect the actual image and repeat this matrix at the next release, and investigate any resulting failure before changing labels.
