# Security and testing

This is a public, read-only portfolio, not a login shell. Visitors aren't authenticated, and anything shown can be copied. Keep private data out of it.

The server rejects commands, file transfers, forwarding and environment requests. It limits connections, session time, terminal sizes and input. Those limits help with abuse, but don't make it attack-proof or stop an upstream traffic flood.

## Run the checks

From the repo root, with Go, Python 3 and OpenSSH installed:

```sh
go mod verify
go test -race -count=1 ./...
go vet ./...
go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...
python3 scripts/smoke_test.py
python3 scripts/test_deployment.py
```

The live tests use temporary keys and local ports. Keep abuse tests local, not on someone else's server or a service with real visitors. Passing tests isn't proof of production capacity or complete security.

## When hosting it

Use a separate, patched VPS and the [systemd setup](deploy/README.md). Running the binary directly doesn't apply that sandbox. Keep admin SSH separate and test the service locally before opening it up.

Keep the portfolio host key private and persistent. Don't reuse an admin or personal key, and don't disable host-key checking. Share the fingerprint through a trusted channel.

Monitor usage and restarts, keep Go and dependencies updated, and use host/provider firewall and traffic controls. Don't give the service access to personal files, credentials or other services.

For a security report, share a small local reproduction and affected versions privately. Never post keys or server credentials in an issue.
