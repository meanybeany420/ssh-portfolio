"""Local deployment contract tests; never touch the host's service manager."""
import hashlib
import os
from pathlib import Path
import subprocess
import sys
import tarfile
import tempfile
import unittest

REPO = Path(__file__).resolve().parents[1]
FILES = {'ssh-portfolio', 'deploy/ssh-portfolio.service', 'deploy/install.sh',
         'deploy/README.md', 'SECURITY.md', 'LICENSE'}


def read_member(archive, name):
    stream = archive.extractfile(name)
    assert stream is not None, name
    return stream.read()


class ReleaseTests(unittest.TestCase):
    def test_rejects_bad_output_and_architecture(self):
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / 'release.tar.gz'
            output.write_text('do not overwrite')
            for arguments in (['--arch', 'x86_64', '--output', str(output)],
                              ['--arch', 'amd64', '--output', str(output)],
                              ['--arch', 'amd64', '--output', str(output.with_suffix('.zip'))],
                              ['--arch', 'arm64', '--output', str(Path(directory) / 'missing/release.tar.gz')],
                              ['--arch', 'amd64'],
                              ['--arch', 'amd64', '--output', str(Path(directory) / 'bad\nname.tar.gz')]):
                with self.subTest(arguments=arguments):
                    result = subprocess.run([sys.executable, str(REPO / 'scripts/build_release.py'), *arguments],
                                            capture_output=True, text=True)
                    self.assertNotEqual(result.returncode, 0)
            self.assertEqual(output.read_text(), 'do not overwrite')

    def test_build_archive(self):
        with tempfile.TemporaryDirectory() as directory:
            archive = Path(directory) / 'release.tar.gz'
            result = subprocess.run([sys.executable, str(REPO / 'scripts/build_release.py'),
                                     '--arch', 'amd64', '--output', str(archive)],
                                    cwd=directory, capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            with tarfile.open(archive) as bundle:
                self.assertEqual(set(bundle.getnames()), FILES | {'SHA256SUMS'})
                manifest = read_member(bundle, 'SHA256SUMS').decode()
                listed = {}
                for line in manifest.splitlines():
                    digest, name = line.split('  ')
                    listed[name] = digest
                self.assertEqual(set(listed), FILES)
                for name, digest in listed.items():
                    self.assertEqual(hashlib.sha256(read_member(bundle, name)).hexdigest(), digest)
                self.assertTrue(all(member.isfile() for member in bundle.getmembers()))
                binary = read_member(bundle, 'ssh-portfolio')
                self.assertEqual(binary[:4], b'\x7fELF')
                self.assertEqual(binary[18:20], b'\x3e\x00')
            self.assertEqual(archive.with_name(archive.name + '.sha256').read_text(),
                             hashlib.sha256(archive.read_bytes()).hexdigest() + '  release.tar.gz\n')
            extracted = Path(directory) / 'extracted'
            extracted.mkdir()
            with tarfile.open(archive) as bundle:
                bundle.extractall(extracted, filter='data')
            staging = Path(directory) / 'staging'
            staging.mkdir(mode=0o700)
            command = ['sh', str(extracted / 'deploy/install.sh'), '--root', str(staging)]
            result = subprocess.run(command, cwd=directory, capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            key = staging / 'etc/ssh-portfolio/hostkey'
            original = key.read_bytes()
            result = subprocess.run([*command, '--upgrade'], cwd=directory, capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(key.read_bytes(), original)
            self.assertEqual((staging / 'opt/ssh-portfolio/ssh-portfolio').read_bytes(), binary)

    def test_build_arm64_archive(self):
        with tempfile.TemporaryDirectory() as directory:
            archive = Path(directory) / 'arm64.tar.gz'
            result = subprocess.run([sys.executable, str(REPO / 'scripts/build_release.py'),
                                     '--arch', 'arm64', '--output', str(archive)],
                                    cwd=directory, capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            with tarfile.open(archive) as bundle:
                self.assertEqual(set(bundle.getnames()), FILES | {'SHA256SUMS'})
                binary = read_member(bundle, 'ssh-portfolio')
                self.assertEqual(binary[:4], b'\x7fELF')
                self.assertEqual(binary[18:20], b'\xb7\x00')


class InstallerTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.base = Path(self.temp.name)
        self.bundle = self.base / 'bundle'
        self.root = self.base / 'root'
        self.root.mkdir(mode=0o700)
        self.bundle.mkdir()
        for name in FILES:
            target = self.bundle / name
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_bytes(b'test binary\n' if name == 'ssh-portfolio' else (REPO / name).read_bytes())
        self.manifest()

    def manifest(self):
        (self.bundle / 'SHA256SUMS').write_text(''.join(
            hashlib.sha256((self.bundle / name).read_bytes()).hexdigest() + '  ' + name + '\n'
            for name in sorted(FILES)))

    def install(self, *args):
        return subprocess.run(['sh', str(self.bundle / 'deploy/install.sh'), '--root',
                               str(self.root), *args], cwd=self.base,
                              capture_output=True, text=True)

    def test_rejects_tampered_manifest_and_payload(self):
        path = self.bundle / 'SHA256SUMS'
        original = path.read_text()
        for malicious in (original + original.splitlines()[0] + '\n',
                          original.replace('  LICENSE', '  ../escape'),
                          original.replace('  LICENSE', '  unlisted'),
                          '\n'.join(original.splitlines()[1:]) + '\n'):
            with self.subTest(manifest=malicious):
                path.write_text(malicious)
                self.assertNotEqual(self.install().returncode, 0)
                self.assertFalse((self.root / 'opt').exists())
        path.write_text(original)
        (self.bundle / 'ssh-portfolio').write_bytes(b'tampered')
        self.assertNotEqual(self.install().returncode, 0)
        self.assertFalse((self.root / 'opt').exists())

    def test_rejects_non_loopback_unit(self):
        path = self.bundle / 'deploy/ssh-portfolio.service'
        path.write_text(path.read_text().replace('127.0.0.1:23234', '0.0.0.0:23234'))
        self.manifest()
        self.assertNotEqual(self.install().returncode, 0)
        self.assertFalse((self.root / 'opt').exists())

    def test_rejects_symlink_destination(self):
        outside = self.base / 'outside'
        outside.mkdir()
        (self.root / 'opt').symlink_to(outside, target_is_directory=True)
        self.assertNotEqual(self.install().returncode, 0)
        self.assertEqual(list(outside.iterdir()), [])

    def test_rejects_unsafe_directory(self):
        path = self.root / 'opt'
        path.mkdir(mode=0o777)
        path.chmod(0o777)
        self.assertNotEqual(self.install().returncode, 0)
        self.assertFalse((self.root / 'etc').exists())

    def test_rejects_unrelated_installation(self):
        app = self.root / 'opt/ssh-portfolio'
        app.mkdir(parents=True)
        unrelated = app / 'ssh-portfolio'
        unrelated.write_text('unrelated data')
        self.assertNotEqual(self.install('--upgrade').returncode, 0)
        self.assertEqual(unrelated.read_text(), 'unrelated data')
        self.assertFalse((self.root / 'etc').exists())

    def test_rejects_existing_unit_dropins(self):
        dropins = self.root / 'etc/systemd/system/ssh-portfolio.service.d'
        dropins.mkdir(parents=True)
        override = dropins / 'override.conf'
        override.write_text('[Service]\nExecStart=\nExecStart=/unrelated\n')
        self.assertNotEqual(self.install().returncode, 0)
        self.assertTrue(override.exists())
        self.assertFalse((self.root / 'opt').exists())

    def test_rejects_unrelated_unit(self):
        target = self.root / 'etc/systemd/system/ssh-portfolio.service'
        target.parent.mkdir(parents=True)
        target.write_text('unrelated unit')
        self.assertNotEqual(self.install().returncode, 0)
        self.assertEqual(target.read_text(), 'unrelated unit')
        self.assertFalse((self.root / 'opt').exists())

    def test_rejects_runtime_tampering(self):
        result = self.install()
        self.assertEqual(result.returncode, 0, result.stderr)
        binary = self.root / 'opt/ssh-portfolio/ssh-portfolio'
        binary.write_text('runtime tampering')
        self.assertNotEqual(self.install('--upgrade').returncode, 0)
        self.assertEqual(binary.read_text(), 'runtime tampering')

    def test_rejects_changed_unit_and_missing_key_on_upgrade(self):
        result = self.install()
        self.assertEqual(result.returncode, 0, result.stderr)
        unit = self.root / 'etc/systemd/system/ssh-portfolio.service'
        original = unit.read_bytes()
        unit.write_text('local configuration')
        self.assertNotEqual(self.install('--upgrade').returncode, 0)
        self.assertEqual(unit.read_text(), 'local configuration')
        unit.write_bytes(original)
        key = self.root / 'etc/ssh-portfolio/hostkey'
        key.unlink()
        self.assertNotEqual(self.install('--upgrade').returncode, 0)
        self.assertFalse(key.exists())

    def test_rejects_malformed_arguments(self):
        for arguments in (['--unknown'], ['--upgrade=yes'], ['--root', '/'],
                          ['--root', 'relative'], ['--root'], ['--root', str(self.root / '../root')]):
            with self.subTest(arguments=arguments):
                self.assertNotEqual(self.install(*arguments).returncode, 0)
        self.assertFalse((self.root / 'opt').exists())

    def test_existing_key_is_never_replaced(self):
        keydir = self.root / 'etc/ssh-portfolio'
        keydir.mkdir(parents=True, mode=0o700)
        key = keydir / 'hostkey'
        subprocess.run(['ssh-keygen', '-q', '-t', 'ed25519', '-N', '', '-f', str(key)], check=True)
        original = key.read_bytes()
        result = self.install()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(key.read_bytes(), original)

    def test_rejects_unsafe_existing_key(self):
        keydir = self.root / 'etc/ssh-portfolio'
        keydir.mkdir(parents=True, mode=0o700)
        key = keydir / 'hostkey'
        key.write_text('not a private key')
        key.chmod(0o600)
        self.assertNotEqual(self.install().returncode, 0)
        self.assertEqual(key.read_text(), 'not a private key')
        key.chmod(0o644)
        self.assertNotEqual(self.install().returncode, 0)
        key.unlink()
        outside = self.base / 'outside-key'
        outside.write_text('preserve me')
        key.symlink_to(outside)
        self.assertNotEqual(self.install().returncode, 0)
        self.assertEqual(outside.read_text(), 'preserve me')

    def test_staging_never_calls_systemd_tools(self):
        tools = self.base / 'tools'
        tools.mkdir()
        marker = self.base / 'unexpected-systemd-call'
        for name in ('systemctl', 'systemd-analyze'):
            tool = tools / name
            tool.write_text('#!/bin/sh\ntouch "' + str(marker) + '"\nexit 99\n')
            tool.chmod(0o755)
        env = dict(os.environ, PATH=str(tools) + os.pathsep + os.environ['PATH'])
        result = subprocess.run(['sh', str(self.bundle / 'deploy/install.sh'), '--root', str(self.root)],
                                cwd=self.base, env=env, capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse(marker.exists())

    def test_install_upgrade_preserves_key(self):
        first = self.install()
        self.assertEqual(first.returncode, 0, first.stderr)
        key = self.root / 'etc/ssh-portfolio/hostkey'
        original = key.read_bytes()
        self.assertEqual(key.stat().st_mode & 0o777, 0o600)
        self.assertEqual(key.parent.stat().st_mode & 0o777, 0o700)
        self.assertIn(b'OPENSSH PRIVATE KEY', original)
        self.assertEqual((self.root / 'opt/ssh-portfolio/ssh-portfolio').stat().st_mode & 0o777, 0o755)
        self.assertNotEqual(self.install().returncode, 0)
        (self.bundle / 'ssh-portfolio').write_bytes(b'updated binary\n')
        self.manifest()
        upgrade = self.install('--upgrade')
        self.assertEqual(upgrade.returncode, 0, upgrade.stderr)
        self.assertEqual(key.read_bytes(), original)
        self.assertEqual((self.root / 'opt/ssh-portfolio/ssh-portfolio').read_bytes(), b'updated binary\n')
        self.assertFalse((self.root / 'etc/systemd/system/multi-user.target.wants').exists())


if __name__ == '__main__':
    unittest.main()
