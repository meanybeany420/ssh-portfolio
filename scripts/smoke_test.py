#!/usr/bin/env python3
"""Exercise the real SSH UI locally; all generated keys stay in a temporary directory."""
import argparse
import fcntl
import json
import os
from pathlib import Path
import pty
import re
import select
import shutil
import signal
import struct
import subprocess
import tempfile
import termios
import time

ROOT = Path(__file__).resolve().parents[1]
CSI = re.compile(rb'\x1b\[[0-?]*[ -/]*[@-~]')
OSC = re.compile(rb'\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)')


def plain(data):
    return CSI.sub(b'', OSC.sub(b'', data)).decode('utf-8', errors='replace')


class Visitor:
    def __init__(self, port, known_hosts):
        self.output = bytearray()
        self.master, slave = pty.openpty()
        # A zero-sized PTY can produce an apparently blank Bubble Tea screen.
        fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack('HHHH', 40, 120, 0, 0))
        command = ['ssh', '-F', os.devnull, '-tt', '-p', str(port),
                   '-o', 'BatchMode=yes', '-o', 'ConnectTimeout=5',
                   '-o', 'StrictHostKeyChecking=accept-new',
                   '-o', f'UserKnownHostsFile={known_hosts}',
                   '-o', f'GlobalKnownHostsFile={os.devnull}',
                   '-o', 'IdentityFile=none', '-o', 'IdentityAgent=none',
                   '-o', 'IdentitiesOnly=yes', '-o', 'ForwardAgent=no',
                   '-o', 'ClearAllForwardings=yes', '-o', 'LogLevel=ERROR',
                   'visitor@127.0.0.1']
        env = dict(os.environ, TERM='xterm-256color', COLORTERM='truecolor')
        try:
            self.process = subprocess.Popen(command, stdin=slave, stdout=slave,
                                            stderr=slave, env=env, close_fds=True)
        finally:
            os.close(slave)

    def read(self, timeout):
        if not select.select([self.master], [], [], timeout)[0]:
            return False
        try:
            chunk = os.read(self.master, 65536)
        except OSError:
            return False
        self.output.extend(chunk)
        return bool(chunk)

    def expect(self, text, since=0, timeout=10):
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            if text in plain(bytes(self.output[since:])):
                return
            if self.process.poll() is not None:
                self.read(0)
                break
            self.read(0.1)
        raise AssertionError(f'SSH UI did not render {text!r}:\n{plain(bytes(self.output))}')

    def press(self, keys, expected):
        start = len(self.output)
        os.write(self.master, keys)
        self.expect(expected, since=start)

    def quit(self):
        os.write(self.master, b'q')
        result = self.process.wait(timeout=5)
        if result != 0:
            raise AssertionError(f'SSH visitor exited with {result}')

    def close(self):
        if self.process.poll() is None:
            self.process.terminate()
            try:
                self.process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                self.process.kill()
                self.process.wait(timeout=5)
        os.close(self.master)


def run(binary, capture=None):
    if not shutil.which('ssh'):
        raise RuntimeError('The OpenSSH client is required for the smoke test.')
    with tempfile.TemporaryDirectory(prefix='ssh-portfolio-smoke-') as directory:
        scratch = Path(directory)
        if binary is None:
            binary = scratch / 'ssh-portfolio'
            subprocess.run(['go', 'build', '-buildvcs=false', '-o', str(binary), './cmd'],
                           cwd=ROOT, check=True, timeout=120)
        binary = binary.resolve()
        log_path = scratch / 'server.log'
        visitors = []
        with log_path.open('w') as log:
            server = subprocess.Popen([str(binary), '-listen', '127.0.0.1:0',
                                       '-host-key', str(scratch / 'server-key')],
                                      cwd=scratch, stdout=log, stderr=subprocess.STDOUT)
        try:
            deadline = time.monotonic() + 10
            match = None
            while time.monotonic() < deadline:
                match = re.search(r'listening on 127\.0\.0\.1:(\d+)', log_path.read_text())
                if match:
                    break
                if server.poll() is not None:
                    raise AssertionError('Server startup failed:\n' + log_path.read_text())
                time.sleep(0.05)
            if not match:
                raise AssertionError('Server did not report its bound address:\n' + log_path.read_text())
            port = int(match.group(1))
            first = Visitor(port, scratch / 'known_hosts')
            visitors.append(first)
            first.expect('Select a section:')
            first.press(b'\r', 'University of Chester')
            while first.read(0.25):
                pass
            if capture:
                capture.parent.mkdir(parents=True, exist_ok=True)
                capture.write_bytes(first.output)
            first.press(b'\x1b', 'Select a section:')
            first.press(b'j\r', 'Kennel Reservation Application')
            first.press(b'\x1b', 'Select a section:')
            first.press(b'jjj\r', 'github.com/meanybeany420')
            first.quit()
            if server.poll() is not None:
                raise AssertionError('Visitor quit unexpectedly stopped the server')
            second = Visitor(port, scratch / 'known_hosts')
            visitors.append(second)
            second.expect('Select a section:')
            second.quit()
            server.send_signal(signal.SIGTERM)
            result = server.wait(timeout=8)
            if result != 0:
                raise AssertionError(f'SIGTERM shutdown exited with {result}:\n{log_path.read_text()}')
            return {'sessions': len(visitors), 'menu_navigation': 'passed',
                    'contact_handle': 'passed', 'visitor_quit_keeps_server_running': 'passed',
                    'sigterm_shutdown': 'passed', 'pty': {'columns': 120, 'rows': 40}}
        finally:
            for visitor in visitors:
                visitor.close()
            if server.poll() is None:
                server.terminate()
                try:
                    server.wait(timeout=8)
                except subprocess.TimeoutExpired:
                    server.kill()
                    server.wait(timeout=5)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', type=Path, help='use an already-built server instead of building one')
    parser.add_argument('--capture', type=Path, help='save the real About-screen ANSI stream for a screenshot')
    args = parser.parse_args()
    print(json.dumps(run(args.binary, args.capture), indent=2))


if __name__ == '__main__':
    main()
