# Security model and deployment boundary

## What this application is

An intentionally anonymous, read-only portfolio viewer, **not** a remote administration service. Visitors may navigate the menu and disconnect. Usernames are labels, not identities or Linux accounts. Do not add private data, secrets, uploads, server-side command execution, a file browser, or access to other services without a separate security design.

A public SSH endpoint necessarily accepts and parses untrusted network traffic. It cannot be made "uninteractable" while remaining publicly usable, and neither tests nor a clean vulnerability scan prove it attack-proof. An SSH exploit, a dependency regression, or denial of service remains possible. Keep the default loopback binding until you deliberately deploy an isolated instance.

## Threat review

| Threat | Control / deployment requirement |
| --- | --- |
| Arbitrary exec, shell escape, SCP/SFTP, file reads/writes | Only the portfolio UI is intended to run. Explicit request/channel restrictions and regression tests enforce the protocol boundary; no system shell or file transfer subsystem is needed. |
| SSH used as a proxy into private infrastructure | Deny forwarding, agent/X11 and non-session channels; restrict egress separately on the host where available. |
| Untrusted environment or terminal metadata | Reject environment requests and validate PTY metadata before the renderer. Do not echo usernames, commands, or raw attacker-supplied metadata into a visitor's terminal or logs. |
| Huge window sizes / allocation abuse | Bound terminal dimensions before the UI and again in model updates. A pathological advertised terminal must not drive unbounded screen allocation. |
| Idle connections, slow handshakes, many clients/channels, input/request floods | Application connection/channel caps, time limits and per-connection budgets reduce abuse. Provider/firewall controls are still needed; limits can be deliberately consumed to deny genuine visitors. |
| Host-key theft, MITM, loss of identity on restart | Keep a persistent, private portfolio-only host key, distribute its fingerprint separately, and do not disable client host verification. The running process necessarily has access to its signing key. |
| Runtime compromise | Use the optional unprivileged, read-only systemd sandbox and an isolated VPS. Do not run as root or give the process access to your personal files, SSH agent, cloud credentials or administrator host keys. |
| CPU/RAM/process/file-descriptor exhaustion | Use the service's cgroup and descriptor limits, monitor restarts, and exercise limits on the target host. A memory-limit kill/restart is containment, not availability. |
| Bandwidth/TCP SYN floods or distributed abuse | Cannot be solved inside this Go application. Use upstream DDoS filtering, provider connection limits, host controls and operational monitoring. |
| Go / SSH / terminal parser bugs | Check advisories and `govulncheck`, update and rebuild, keep reproducible tests. No application allowlist eliminates bugs in the transport/parser before the allowlist runs. |
| Malicious dependency / build / CI change | Review dependency and workflow updates; retain module verification and pinned CI actions. A vulnerability scan is not a supply-chain integrity audit. |
| Public personal information harvesting | Anything displayed can be scraped or recorded, including the contact address. Rate limits cannot make public portfolio content private. |

## Enforced application limits

The current server allows one PTY-backed portfolio session per SSH connection lifetime. Only a valid initial `pty-req`, an empty-payload `shell` after the PTY, and valid `window-change` requests are allowed. All other session requests, exec (including an empty command), subsystems, environment requests, forwarding/global requests, and non-session channels are denied. This does not launch a Linux login shell.

- Raw TCP admission, before SSH handshake goroutines: **32 concurrent connections total, 4 per source IP**.
- Timeouts: **5 seconds for handshake, 60 seconds of transport inactivity, 10 minutes absolute lifetime**. Transport inactivity is not the same as user inactivity; incoming traffic can keep an idle transport alive until the absolute limit.
- Terminal bounds: **1–320 columns, 1–120 rows**. Invalid SSH PTY/resizes are rejected; input-stream terminal size reports are clamped before Bubble Tea's renderer, with model-level bounds as a second check. Initial TERM is normalized, terminal modes/pixel sizes discarded.
- Lifetime attempts per connection: **32 channels, 16 global requests, 512 session requests**. Global/session request payloads are capped at **4 KiB** after the SSH transport decodes them.
- Incoming portfolio terminal input: **64 KiB per connection lifetime**, with an **eight-chunk queue of up to 1 KiB per chunk**; overflow disconnects. This deliberately rejects aggressive paste/input floods. The first nonempty inbound stderr/extended type-1 data disconnects, including before PTY or shell startup.
- Separate raw transport ingress allowance: **2 MiB (2,097,152 TCP bytes) per connection lifetime**, counted before the handshake and including identification, ciphertext overhead, unsupported extended-data types, control traffic and rekeys. Reads never deliver bytes beyond this allowance to the SSH parser; reaching the boundary closes the connection and releases its admission slot. The 64 KiB terminal-input allowance alone cannot observe unsupported extended streams, which the upstream library may discard.
- Cryptography uses the installed SSH library's supported algorithm set; RSA host keys, if supplied, sign with SHA-2 rather than legacy RSA/SHA-1. Routine hardening does not rotate existing host keys.

These are conservative service policies, not fair scheduling or traffic-rate guarantees. Visitors behind one NAT share the four-connection cap. Oversized terminals, very rapid paste input, automated keepalives after the global-request budget, or unusually frequent resizes can disconnect/reject otherwise legitimate clients. Reconnect to start a new lifetime budget; connection churn and distributed sources still need provider/host defenses.

## Local verification

Run from the repository root:

```sh
go mod verify
go test -race -count=1 ./...
go vet ./...
go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...
python3 scripts/smoke_test.py
```

The live tests bind only to ephemeral loopback ports and generate isolated temporary keys. Do not direct abuse tests at third-party services or an instance carrying real visitors. Passing these checks does not establish resistance to all possible attacks or a production load capacity.

## Host hardening is separate

See [the optional deployment template](deploy/README.md). The systemd file is **not installed or activated automatically**, and neither its resource limits nor its filesystem isolation apply when running `go run ./cmd` normally. Its default remains localhost. Validate the template with the target systemd version and exercise a running unit before public exposure. Keep OS administrative SSH separate from the anonymous portfolio listener.

## Primary references

- [Go vulnerability database](https://pkg.go.dev/vuln/) and [govulncheck](https://pkg.go.dev/golang.org/x/vuln/cmd/govulncheck)
- [SSH channel/request and server API](https://pkg.go.dev/golang.org/x/crypto/ssh)
- [systemd execution sandbox and credentials](https://www.freedesktop.org/software/systemd/man/latest/systemd.exec.html)
- [systemd resource controls](https://www.freedesktop.org/software/systemd/man/latest/systemd.resource-control.html)

If reporting a suspected vulnerability, do not post private keys or personal server credentials in a public issue. Provide a minimal local reproduction and the affected commit/dependency versions through an appropriate private channel.
