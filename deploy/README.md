# Optional Linux service deployment

These files are a deployment template, not an installation performed by the security review. The service intentionally binds to `127.0.0.1:23234`. Do not enable public access until the tests and host-level controls in `../SECURITY.md` are checked on your actual VPS.

## Prerequisites

Use a dedicated, patched VPS with systemd and cgroup resource controls, not your personal workstation or your administrative SSH service. Build the application on a trusted machine; do not run builds or Git as the runtime account. Keep administrative SSH separate, key-authenticated, and restricted to trusted sources where practical.

The executable is expected at `/opt/ssh-portfolio/ssh-portfolio`, with root ownership and no runtime write permissions. Documentation can be installed alongside it. Use a persistent Ed25519 host key at `/etc/ssh-portfolio/hostkey`, readable only by root; systemd supplies a read-only credential copy to the dynamic service user. Do not use your personal SSH identity or your administrative SSH host key.

Provision directories and a new key only if they do not exist. Never overwrite an existing key without a deliberate rotation plan; preserving it preserves the portfolio's SSH identity. Do not commit private keys. Tell visitors how to verify the host-key fingerprint through a separate trusted channel rather than instructing them to disable host verification.

After reviewing paths and compatibility, an administrator can install this unit as `/etc/systemd/system/ssh-portfolio.service`. Validate it with `systemd-analyze verify`, reload systemd, start the service, inspect its journal, and exercise the SSH UI locally. Syntax verification does not prove sandbox compatibility: test an actual running unit on the target distribution before enabling it on boot.

## Build and verify a release

From the source checkout (Python 3 and Go required):

```sh
go mod verify
go test -race -count=1 ./...
go vet ./...
go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...
python3 scripts/smoke_test.py
python3 scripts/test_deployment.py
mkdir -p dist
python3 scripts/build_release.py --arch amd64 --output "$PWD/dist/ssh-portfolio-linux-amd64.tar.gz"
cd dist
sha256sum -c ssh-portfolio-linux-amd64.tar.gz.sha256
```

Choose `--arch arm64` and a matching filename for an ARM VPS. The bundle contains a statically built Linux executable, this runbook, the unit, installer, security document, license, and a per-file SHA256 manifest. It does not package your checkout, Git metadata or private keys. The builder refuses existing output filenames; use a versioned name for later releases. The checksum detects corruption, **not authenticity**: obtain the archive and expected hash from your trusted build/transfer channel.

## Install on the target VPS

The target needs Python 3, `ssh-keygen`, `systemctl` and `systemd-analyze`. Transfer the archive and checksum through your authenticated administrative SSH connection. Verify the archive checksum on the target before extraction. Extract into a **dedicated root-owned directory**; the installer rejects extra files, symlinks, unsafe ownership/permissions, unrelated installations and unexpected systemd drop-ins. Do not execute an installer from an untrusted bundle.

For example, after transferring a verified amd64 archive, run on the VPS (not your workstation):

```sh
sudo install -d -m 0700 /root/ssh-portfolio-release
sudo tar -xzf ssh-portfolio-linux-amd64.tar.gz -C /root/ssh-portfolio-release
sudo sh /root/ssh-portfolio-release/deploy/install.sh
```

Use a fresh extraction directory for each release. Installation generates a private portfolio-only host key only if none exists, validates an existing key, installs the root-owned application/unit, verifies the unit and reloads systemd. It **does not start or enable the service**, change your firewall, or change administrative SSH.

For an unprivileged rehearsal, extract a trusted archive into a dedicated directory and use an existing, private, absolute staging directory:

```sh
mkdir -m 0700 /absolute/path/to/staging
sh /absolute/path/to/extracted/deploy/install.sh --root /absolute/path/to/staging
```

The real root/systemd installation path is not exercised by staged tests. On the actual VPS, review the installed unit and first start it with its loopback binding:

```sh
sudo systemctl start ssh-portfolio.service
sudo systemctl status ssh-portfolio.service
sudo journalctl -u ssh-portfolio.service -n 50 --no-pager
sudo ssh-keygen -y -f /etc/ssh-portfolio/hostkey | ssh-keygen -lf -
ssh -p 23234 visitor@127.0.0.1
```

Check the fingerprint, menu navigation, terminal resize, disconnect/reconnect, service resource usage and logs. Only enable on boot (`sudo systemctl enable ssh-portfolio.service`) after the running sandbox has passed these target-host checks. If it fails, stop it and inspect the journal rather than disabling sandbox controls blindly.

## Updates and rollback

Keep the previous verified release. Run a verified new release's installer with `--upgrade` to replace an unmodified existing installation while preserving the host key. **Stop the service first**, and start/check it after the installer succeeds; daemon-reload alone does not replace a running executable. The same explicit upgrade mechanism can restore a previous trusted bundle. Installation is not a transactional rollback system: interruption can leave a partial installation requiring manual recovery.

The installer deliberately refuses modified installed files, a modified unit or systemd drop-ins. Once you customize the unit for public access, updates require manual review/reconciliation; do not delete local hardening or overrides blindly to make the installer proceed. Keep the persistent host key backed up securely and never remove or replace it during routine updates.

## Sandbox intent

`ssh-portfolio.service` uses a dynamic unprivileged user, no capabilities, no privilege escalation, a read-only system and home isolation, inaccessible `/tmp` and `/var/tmp`, private devices and IPC, syscall restrictions, no core dumps, and CPU/memory/task/file-descriptor ceilings. It allows creation of IPv4/IPv6 sockets only, not Unix sockets. `LoadCredential` allows the application to read its identity without granting runtime access to the root-owned source file. These controls require a compatible systemd/kernel configuration; they are not enabled by running the Go binary directly.

Read-only filesystem mounts alone do not block communication with host Unix sockets. The address-family restriction adds a separate barrier, but inherited descriptors remain available (including journal output). Audit the target service's descriptors, mounts and accessible host IPC endpoints; do not pass extra sockets or credentials to it. The sandbox is defense in depth, not equivalent to a separate VM.

Memory and CPU limits are conservative starting points, not capacity guarantees. Observe normal usage and test the service under expected load before adjusting them. The syscall policy must also be exercised on the target host. An attacker who exhausts the process memory limit can still cause a restart; the restart burst limit prevents unlimited rapid restart loops but cannot guarantee availability.

## Deliberately enabling public access

Only after a separate deployment decision, change the unit's `-listen` address to a wildcard such as `0.0.0.0:23234`. Go and the operating system may implement this as a dual-stack listener; the spelling alone does not establish IPv4-only exposure. Inspect the actual listener and global addresses, review both provider and host firewalls, and test IPv4 and IPv6 independently. Keep administrative SSH separate and restrict its sources where practical.

Use provider-side connection/rate controls and DDoS protection where available: application limits cannot stop an upstream bandwidth flood or TCP SYN flood. Restrict outbound new connections at the host/provider layer if supported, while preserving established connection responses and required host maintenance. Do not apply generic firewall commands blindly to a remote server: retain an administrative connection and a recovery console.

Monitor resource usage/restarts, install OS security updates, rebuild after relevant Go/library fixes, and keep a previous known-good binary for rollback. Re-run `go test -race ./...`, `go vet ./...`, `govulncheck`, and `scripts/smoke_test.py` after upgrades.

## Using port 22 for visitors

The supplied unit keeps its high loopback port and no capabilities. To use port 22 publicly, first move administrative SSH to a separate key-authenticated port and verify a fresh administrator login. Keep the working session, a recovery console and a backup available. On socket-activated distributions, inspect `ssh.socket` as well as `sshd_config`: changing the latter alone may not move the actual listeners. Review both firewalls before changing ports; do not stop or replace the administrator listener blindly.

Only after port 22 is free, an explicitly reviewed service drop-in can grant the single capability required for a privileged port:

```ini
[Service]
ExecStart=
ExecStart=/opt/ssh-portfolio/ssh-portfolio -listen 0.0.0.0:22 -host-key %d/hostkey
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
AmbientCapabilities=CAP_NET_BIND_SERVICE
```

Keep `DynamicUser=yes`, `NoNewPrivileges=yes`, the credential loading and all other sandbox/resource controls. Do not run the portfolio as root or lower the host-wide unprivileged-port threshold. Validate the effective unit, restart only the portfolio, then check the actual listeners and test both visitor and administrator connections again. A separate administrative port is not itself an authentication or firewall control.

Preserve the existing portfolio host key. A client that previously used port 22 for administrative SSH will see a legitimate service-identity change, but must still compare the new fingerprint through the trusted administrator connection before replacing only that old known-host record. Never disable strict host-key checking or delete the entire known-hosts file.

The installer refuses custom drop-ins during automatic upgrades; use the manual review/reconciliation process described above and retain the persistent key and low-port override. Publishing these examples does not change any running host.

Official references: `systemd.exec(5)`, `systemd.resource-control(5)`, `systemd.service(5)`, and `systemd-analyze(1)`.
