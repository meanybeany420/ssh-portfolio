#!/usr/bin/env python3
"""Build an allowlisted Linux release, never a snapshot of the checkout."""
import argparse
import hashlib
import io
import os
import re
from pathlib import Path
import subprocess
import tarfile
import tempfile

REPO = Path(__file__).resolve().parents[1]
SOURCES = ('deploy/ssh-portfolio.service', 'deploy/install.sh',
           'deploy/README.md', 'SECURITY.md', 'LICENSE')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--arch', choices=('amd64', 'arm64'), required=True)
    parser.add_argument('--output', type=Path, required=True, help='new .tar.gz path; parent must exist')
    args = parser.parse_args()
    output = args.output.absolute()
    checksum = output.with_name(output.name + '.sha256')
    if (not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9._-]*\.tar\.gz', output.name)
            or '..' in output.parts or not output.parent.is_dir()):
        parser.error('--output must have a safe alphanumeric .tar.gz filename in an existing directory (no ..)')
    for path in (output, checksum):
        if path.exists() or path.is_symlink():
            parser.error(f'refusing to overwrite {path}')
    for path in (output.parent, *output.parent.parents):
        if path.is_symlink():
            parser.error('output parent must not contain symlinks')
    payload = {}
    for name in SOURCES:
        path = REPO / name
        if not path.is_file() or any(p.is_symlink() for p in (path, *path.parents)):
            parser.error(f'unsafe or missing release input: {name}')
        payload[name] = path.read_bytes()
    with tempfile.TemporaryDirectory(prefix='ssh-portfolio-build-') as directory:
        binary = Path(directory) / 'ssh-portfolio'
        env = dict(os.environ, CGO_ENABLED='0', GOOS='linux', GOARCH=args.arch,
                   GOFLAGS='', GOEXPERIMENT='')
        subprocess.run(['go', 'build', '-trimpath', '-o', str(binary), './cmd'],
                       cwd=REPO, env=env, check=True)
        payload['ssh-portfolio'] = binary.read_bytes()
        payload['SHA256SUMS'] = ''.join(
            f'{hashlib.sha256(payload[name]).hexdigest()}  {name}\n'
            for name in sorted(payload)).encode()
        staged = Path(directory) / 'release.tar.gz'
        with tarfile.open(staged, 'w:gz') as archive:
            for name in sorted(payload):
                member = tarfile.TarInfo(name)
                member.size = len(payload[name])
                member.mode = 0o755 if name in ('ssh-portfolio', 'deploy/install.sh') else 0o644
                member.uid = member.gid = member.mtime = 0
                member.uname = member.gname = 'root'
                archive.addfile(member, io.BytesIO(payload[name]))
        # Exclusive creation protects preexisting outputs, including dangling links.
        with output.open('xb') as stream:
            stream.write(staged.read_bytes())
        with checksum.open('x') as stream:
            stream.write(f'{hashlib.sha256(output.read_bytes()).hexdigest()}  {output.name}\n')
    print(output)


if __name__ == '__main__':
    main()
