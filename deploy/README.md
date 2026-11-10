# Running it on a VPS

The supplied systemd service starts on `127.0.0.1:23234`. It's a starting point, not a ready-made public deployment. Keep admin SSH separate.

Use a patched Linux VPS with systemd and resource controls. The target also needs Python 3 and OpenSSH's `ssh-keygen`. Build on a trusted machine, not as the service user.

## Build a release

Run the checks in [SECURITY.md](../SECURITY.md), then from the repo root:

```sh
mkdir -p dist
python3 scripts/build_release.py --arch amd64 --output "$PWD/dist/ssh-portfolio-linux-amd64.tar.gz"
cd dist
sha256sum -c ssh-portfolio-linux-amd64.tar.gz.sha256
```

For ARM, use `--arch arm64` and a matching filename. The builder won't overwrite an archive, so use a new name for later releases.

Transfer the archive and checksum through trusted admin SSH. A matching checksum checks corruption, not who made the archive.

## Install and try it

On the VPS, verify the checksum again, then extract into a fresh, root-owned directory:

```sh
sha256sum -c ssh-portfolio-linux-amd64.tar.gz.sha256
sudo install -d -m 0700 /root/ssh-portfolio-release
sudo tar -xzf ssh-portfolio-linux-amd64.tar.gz -C /root/ssh-portfolio-release
sudo sh /root/ssh-portfolio-release/deploy/install.sh
```

Use a new extraction directory for each release. Only run an installer from a trusted bundle.

It installs the app under `/opt/ssh-portfolio`, validates the unit and reloads systemd. The private host key lives at `/etc/ssh-portfolio/hostkey`; existing keys are kept. It doesn't start the service, open ports or change admin SSH.

```sh
sudo systemctl start ssh-portfolio.service
sudo systemctl status ssh-portfolio.service
sudo journalctl -u ssh-portfolio.service -n 50 --no-pager
sudo ssh-keygen -y -f /etc/ssh-portfolio/hostkey | ssh-keygen -lf -
ssh -p 23234 visitor@127.0.0.1
```

Check the fingerprint, navigation, resizing, reconnects, logs and resource use. Only then enable it at boot:

```sh
sudo systemctl enable ssh-portfolio.service
```

If it fails, check the journal rather than stripping out sandbox settings. The unit limits privileges, filesystem access, sockets and resources, but still needs testing on your actual host.

## Public access

After local checks, review the unit's `-listen` address and both host/provider firewalls. Check actual listeners and test IPv4 and IPv6 separately; a wildcard address can expose both. Keep a working admin connection and recovery console while changing networking.

For visitor access on port 22, first move admin SSH and verify a fresh admin login. Check `ssh.socket` too if the host uses socket activation. Only free port 22 after that works. A reviewed service override needs `CAP_NET_BIND_SERVICE` to bind the low port; keep the dynamic user and other sandbox controls, don't run the portfolio as root.

Preserve the portfolio key and verify its fingerprint through trusted admin SSH. Don't disable host checking or wipe known-hosts to hide a key change.

Use provider traffic protection where available, review outbound access and monitor restarts. The app's limits can't stop a network flood.

## Updates

Keep a previous trusted release and a private backup of the host key. Stop the service, run the new bundle's installer with `--upgrade`, then start and check it again. Re-run the checks after code or dependency updates.

The installer refuses modified installed files, units and systemd drop-ins. Custom public-port setups need manual review; don't delete overrides just to force an upgrade. A previous trusted bundle can be restored the same way, but installation isn't transactional and an interrupted update may need manual repair.
