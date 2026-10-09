# Contributing

Read [AGENTS.md](AGENTS.md) and the relevant design documents first. Keep changes focused and preserve terminal, mobile keyboard, account isolation and persistent-tab behavior.

Use Go 1.26.9 or newer. Run `go test -race ./...`, `go vet ./...`, `golangci-lint run ./...` and `govulncheck ./...`. Keep full diagnostics and explain any retained finding. Run the real affected path; terminal changes require a real ttyd WebSocket handshake and input/output check.

Do not add private hosts, personal addresses, credentials, database files or screenshots containing personal data. Run `python3 scripts/check-public.py` before publishing. Report vulnerabilities privately using GitHub's private vulnerability reporting when enabled; otherwise contact the maintainer through the profile linked by this repository.

Build packages locally with `python3 scripts/release.py --version VERSION`. This does not depend on CI. ShellGate code is MIT; dependencies retain their own licenses.
