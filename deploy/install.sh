#!/bin/sh
# Run manually from a trusted extracted release. Requires Python 3 and ssh-keygen.
# No start/enable, firewall, or administrative SSH changes are performed.
set -eu
exec python3 - "$0" "$@" <<'PY'
import argparse
import hashlib
import os
from pathlib import Path
import re
import shutil
import stat
import subprocess
import sys
import tempfile

FILES = {'ssh-portfolio', 'deploy/ssh-portfolio.service', 'deploy/install.sh',
         'deploy/README.md', 'SECURITY.md', 'LICENSE'}
UNIT = 'deploy/ssh-portfolio.service'


def fail(message):
    raise SystemExit('install: ' + message)


def digest(data):
    return hashlib.sha256(data).hexdigest()


def regular(path, private=False):
    info = path.lstat()
    if not stat.S_ISREG(info.st_mode) or info.st_nlink != 1:
        fail(f'not a single-link regular file: {path}')
    if info.st_uid != os.geteuid() or info.st_mode & 0o022:
        fail(f'unsafe owner/permissions: {path}')
    if private and stat.S_IMODE(info.st_mode) != 0o600:
        fail(f'key must have mode 0600: {path}')


def directory(path):
    info = path.lstat()
    if not stat.S_ISDIR(info.st_mode) or info.st_uid != os.geteuid() or info.st_mode & 0o022:
        fail(f'unsafe directory: {path}')


def manifest(path):
    regular(path)
    entries = {}
    for line in path.read_text().splitlines():
        match = re.fullmatch(r'([0-9a-f]{64})  (.+)', line)
        if not match or match[2] not in FILES or match[2] in entries:
            fail(f'invalid allowlisted manifest: {path}')
        entries[match[2]] = match[1]
    if entries.keys() != FILES:
        fail(f'incomplete manifest: {path}')
    return entries


def checked_tree(base, expected):
    directory(base)
    directory(base / 'deploy')
    if {p.name for p in base.iterdir()} != {'ssh-portfolio', 'deploy', 'SECURITY.md', 'LICENSE', 'SHA256SUMS'}:
        fail(f'unexpected files in {base}')
    if {p.name for p in (base / 'deploy').iterdir()} != {'ssh-portfolio.service', 'install.sh', 'README.md'}:
        fail(f'unexpected deployment files in {base}')
    for name, checksum in expected.items():
        regular(base / name)
        if digest((base / name).read_bytes()) != checksum:
            fail(f'checksum mismatch: {base / name}')


def atomic_write(path, data, mode):
    fd, temp = tempfile.mkstemp(prefix='.install-', dir=path.parent)
    try:
        with os.fdopen(fd, 'wb') as stream:
            stream.write(data)
            stream.flush()
            os.fchmod(stream.fileno(), mode)
            os.fsync(stream.fileno())
        os.replace(temp, path)
    finally:
        if os.path.exists(temp):
            os.unlink(temp)


def main():
    parser = argparse.ArgumentParser(description='Install only; never start or enable the service.')
    parser.add_argument('--root', type=Path, help='existing, private staging directory; no systemctl, no root needed')
    parser.add_argument('--upgrade', action='store_true', help='replace a verified prior installation, preserving the key')
    args = parser.parse_args(sys.argv[2:])
    staging = args.root is not None
    if not staging and os.geteuid() != 0:
        fail('normal installation requires explicit root privileges (sudo sh deploy/install.sh)')
    root = args.root.absolute() if staging else Path('/')
    if staging and (not args.root.is_absolute() or root == Path('/') or '..' in root.parts):
        fail('--root must be an absolute staging directory other than /, without ..')
    for path in (root, *root.parents):
        if path.is_symlink():
            fail(f'symlink in staging path: {path}')
    directory(root)
    bundle = Path(os.path.abspath(sys.argv[1])).parent.parent
    for path in (bundle, *bundle.parents):
        if path.is_symlink():
            fail(f'symlink in bundle path: {path}')
    expected = manifest(bundle / 'SHA256SUMS')
    checked_tree(bundle, expected)
    payload = {name: (bundle / name).read_bytes() for name in FILES}
    unit = payload[UNIT].decode()
    commands = [line for line in unit.splitlines() if line.startswith('Exec')]
    if commands != ['ExecStart=/opt/ssh-portfolio/ssh-portfolio -listen 127.0.0.1:23234 -host-key %d/hostkey']:
        fail('release unit must have only the approved loopback ExecStart')
    for setting in ('DynamicUser=yes', 'LoadCredential=hostkey:/etc/ssh-portfolio/hostkey'):
        if unit.splitlines().count(setting) != 1:
            fail('release unit is missing required credential isolation')
    keygen = shutil.which('ssh-keygen')
    if not keygen:
        fail('ssh-keygen is required')
    if not staging:
        if not Path('/run/systemd/system').is_dir() or Path('/proc/1/comm').read_text().strip() != 'systemd':
            fail('normal installation requires a booted systemd host; use --root for staging')
        for tool in ('/usr/bin/systemctl', '/usr/bin/systemd-analyze'):
            regular(Path(tool).resolve())
    app = root / 'opt/ssh-portfolio'
    unit_target = root / 'etc/systemd/system/ssh-portfolio.service'
    keydir = root / 'etc/ssh-portfolio'
    key = keydir / 'hostkey'
    # Refuse preexisting drop-ins instead of silently allowing an unrelated
    # override to replace the loopback command or weaken the sandbox.
    for location in ('etc/systemd/system', 'run/systemd/system', 'usr/lib/systemd/system', 'lib/systemd/system'):
        for name in ('service.d', 'ssh-.service.d', 'ssh-portfolio.service.d'):
            override = root / location / name
            if os.path.lexists(override):
                fail(f'existing systemd drop-in requires manual review: {override}')
    # Validate every existing destination before writing anything. Shared parents
    # are permitted, but may not be writable by the unprivileged runtime.
    for relative in ('opt', 'etc', 'etc/systemd', 'etc/systemd/system', 'etc/ssh-portfolio'):
        path = root / relative
        if os.path.lexists(path):
            directory(path)
    if os.path.lexists(keydir) and stat.S_IMODE(keydir.stat().st_mode) != 0o700:
        fail(f'key directory must have mode 0700: {keydir}')
    if os.path.lexists(key):
        regular(key, private=True)
        subprocess.run([keygen, '-y', '-P', '', '-f', str(key)], check=True,
                       stdout=subprocess.DEVNULL, stdin=subprocess.DEVNULL, timeout=15)
    prior = os.path.lexists(app)
    if prior:
        if not args.upgrade:
            fail('installation exists; use --upgrade after reviewing the new bundle')
        previous = manifest(app / 'SHA256SUMS')
        checked_tree(app, previous)
        regular(unit_target)
        if digest(unit_target.read_bytes()) != previous[UNIT]:
            fail('installed unit changed; refusing to overwrite local configuration')
        if not key.exists():
            fail('existing installation is missing its key; refusing accidental identity rotation')
    elif os.path.lexists(unit_target):
        fail('refusing to overwrite an unrelated systemd unit')
    for relative in ('opt', 'etc', 'etc/systemd', 'etc/systemd/system', 'etc/ssh-portfolio',
                     'opt/ssh-portfolio', 'opt/ssh-portfolio/deploy'):
        path = root / relative
        if not path.exists():
            path.mkdir(mode=0o700 if path == keydir else 0o755)
        directory(path)
    if not key.exists():
        with tempfile.TemporaryDirectory(prefix='.keygen-', dir=keydir) as directory_name:
            generated = Path(directory_name) / 'hostkey'
            subprocess.run([keygen, '-q', '-t', 'ed25519', '-N', '', '-f', str(generated)],
                           check=True, stdin=subprocess.DEVNULL, timeout=30)
            os.chmod(generated, 0o600)
            # Exclusive link, never replacement, even if a key appeared meanwhile.
            os.link(generated, key)
    for name in sorted(FILES):
        atomic_write(app / name, payload[name], 0o755 if name in ('ssh-portfolio', 'deploy/install.sh') else 0o644)
    atomic_write(unit_target, payload[UNIT], 0o644)
    atomic_write(app / 'SHA256SUMS', (bundle / 'SHA256SUMS').read_bytes(), 0o644)
    checked_tree(app, expected)
    regular(unit_target)
    regular(key, private=True)
    if unit_target.read_bytes() != payload[UNIT]:
        fail('unit read-back mismatch')
    for name in FILES:
        mode = 0o755 if name in ('ssh-portfolio', 'deploy/install.sh') else 0o644
        if stat.S_IMODE((app / name).stat().st_mode) != mode:
            fail(f'installed mode mismatch: {name}')
    if not staging:
        subprocess.run(['/usr/bin/systemd-analyze', 'verify', str(unit_target)], check=True)
        subprocess.run(['/usr/bin/systemctl', 'daemon-reload'], check=True)
        for property_name, expected_value in (('FragmentPath', str(unit_target)), ('DropInPaths', '')):
            result = subprocess.run(['/usr/bin/systemctl', 'show', '--property=' + property_name,
                                     '--value', 'ssh-portfolio.service'], check=True,
                                    capture_output=True, text=True)
            if result.stdout.strip() != expected_value:
                fail(f'systemd read-back mismatch for {property_name}; service was NOT started')
    print('Verified installation at ' + str(app))
    print('Key preserved or generated at ' + str(key))
    print('Service NOT started or enabled; firewall and administrative SSH unchanged.')


try:
    main()
except (OSError, ValueError, subprocess.SubprocessError) as error:
    fail(str(error))
PY
