#!/usr/bin/env python3
"""Upload completed scans, verify their bytes, then retain them under uploaded/."""
from pathlib import Path
import argparse
import datetime
import fcntl
import hashlib
import json
import logging
import os
import signal
import stat
import subprocess
import tempfile
import threading
import time

EXTENSIONS = {'.pdf', '.jpg', '.jpeg', '.png', '.tif', '.tiff', '.bmp'}
LOG = logging.getLogger('scan-upload')


def digest(path):
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    with os.fdopen(fd, 'rb') as stream:
        if not stat.S_ISREG(os.fstat(stream.fileno()).st_mode):
            raise ValueError('scan is not a regular file')
        return hashlib.file_digest(stream, 'sha256').hexdigest()


def save_json(path, data):
    fd, temporary = tempfile.mkstemp(prefix='.save-', dir=path.parent)
    try:
        with os.fdopen(fd, 'w') as stream:
            json.dump(data, stream, indent=2)
            stream.write('\n')
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temporary, path)
        directory = os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY)
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


class Uploader:
    def __init__(self, source, state, destination, rclone, config, min_age=10, stopping=lambda: False):
        self.source, self.state = Path(source), Path(state)
        self.archive = self.source / 'uploaded'
        self.destination = destination.rstrip('/')
        self.command = [str(rclone), '--config', str(config)]
        self.options = ['--retries', '1', '--low-level-retries', '2',
                        '--contimeout', '10s', '--timeout', '30s', '--log-level', 'ERROR']
        self.min_age = min_age
        self.stopping = stopping
        self.state.mkdir(mode=0o700, parents=True, exist_ok=True)
        self.archive.mkdir(mode=0o700, exist_ok=True)

    def candidates(self):
        for directory, folders, files in os.walk(self.source, followlinks=False):
            folders[:] = sorted(name for name in folders
                                if not name.startswith('.')
                                and not Path(directory, name).is_symlink()
                                and Path(directory, name) != self.archive)
            for name in sorted(files):
                path = Path(directory, name)
                if (name.startswith('.') or path.suffix.lower() not in EXTENSIONS
                        or path.is_symlink() or not path.is_file()):
                    continue
                if time.time() - path.stat().st_mtime >= self.min_age:
                    yield path

    def upload(self, source, remote):
        if '\n' in source.name or '\r' in source.name:
            raise ValueError('scan filename must not contain a line break')
        # The directory-copy path enforces --immutable for content changes.
        # A literal files-from list selects only this completed scan.
        with tempfile.NamedTemporaryFile(mode='w', dir=self.state) as selection:
            selection.write(source.name + '\n')
            selection.flush()
            result = subprocess.run(self.command + ['copy', str(source.parent), remote.rsplit('/', 1)[0],
                                    '--files-from-raw', selection.name, '--no-traverse', '--immutable'] + self.options,
                                    capture_output=True, timeout=180, text=True)
        if result.returncode:
            raise RuntimeError('rclone copy failed: ' + result.stderr.strip()[-2000:])

    def remote_digest(self, remote, expected_size):
        # SMB does not supply a content hash. Read back the uploaded bytes
        # before recording success; the temporary verification file is private.
        with tempfile.TemporaryFile(dir=self.state) as output:
            result = subprocess.run(self.command + ['cat', remote] + self.options,
                                    stdout=output, stderr=subprocess.PIPE, timeout=180)
            if result.returncode:
                raise RuntimeError('rclone verification read failed: ' + result.stderr.decode(errors='replace')[-2000:])
            if output.tell() != expected_size:
                raise RuntimeError('uploaded file size differs from the source')
            output.seek(0)
            return hashlib.file_digest(output, 'sha256').hexdigest()

    def archive_local(self, source, relative, expected_hash):
        destination = self.archive / relative
        destination.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
        if not destination.parent.resolve().is_relative_to(self.archive.resolve()):
            raise RuntimeError('archive path escapes the uploaded directory')
        if digest(source) != expected_hash:
            raise RuntimeError('scan changed after upload; leaving it in the queue')
        try:
            # A hard link avoids overwriting an older archive and retains the
            # file if a process interruption occurs before the unlink.
            os.link(source, destination, follow_symlinks=False)
        except FileExistsError:
            if digest(destination) != expected_hash:
                raise RuntimeError('a different local archive already uses this filename')
        # Persist the archive link before removing the queue entry.
        fd = os.open(destination.parent, os.O_RDONLY | os.O_DIRECTORY)
        try:
            os.fsync(fd)
        finally:
            os.close(fd)
        os.unlink(source)
        fd = os.open(source.parent, os.O_RDONLY | os.O_DIRECTORY)
        try:
            os.fsync(fd)
        finally:
            os.close(fd)

    def run_once(self):
        lock = self.state / 'upload.lock'
        fd = os.open(lock, os.O_CREAT | os.O_RDWR, 0o600)
        with os.fdopen(fd, 'w') as handle:
            try:
                fcntl.flock(handle, fcntl.LOCK_EX | fcntl.LOCK_NB)
            except BlockingIOError:
                return 0
            receipt_file = self.state / 'receipts.json'
            receipts = json.loads(receipt_file.read_text()) if receipt_file.exists() else {}
            count = 0
            for source in self.candidates():
                if self.stopping():
                    break
                relative = source.relative_to(self.source).as_posix()
                content_hash, size = digest(source), source.stat().st_size
                remote = self.destination + '/' + relative
                receipt = receipts.get(relative, {})
                if receipt.get('sha256') != content_hash or receipt.get('destination') != remote:
                    self.upload(source, remote)
                    if self.remote_digest(remote, size) != content_hash:
                        raise RuntimeError('uploaded SHA-256 differs; scan remains in queue: ' + relative)
                    if digest(source) != content_hash:
                        raise RuntimeError('source changed during transfer: ' + relative)
                    receipts[relative] = {'sha256': content_hash, 'size': size,
                                          'destination': remote,
                                          'verified_at': datetime.datetime.now(datetime.timezone.utc).isoformat()}
                    # Commit the receipt before moving locally. A retry can
                    # finish this move without consulting a reorganized NAS.
                    save_json(receipt_file, receipts)
                self.archive_local(source, relative, content_hash)
                count += 1
                LOG.info('verified and archived %s', relative)
            return count


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--once', action='store_true')
    parser.add_argument('--settings', default='/config/settings.json')
    args = parser.parse_args()
    logging.basicConfig(level=logging.INFO, format='%(asctime)s %(levelname)s %(message)s')
    stopping = threading.Event()
    signal.signal(signal.SIGTERM, lambda *_: stopping.set())
    signal.signal(signal.SIGINT, lambda *_: stopping.set())
    previous_state = None
    while not stopping.is_set():
        try:
            settings = json.loads(Path(args.settings).read_text())
            if not settings.get('destination'):
                if previous_state != 'unconfigured':
                    LOG.info('waiting for NAS configuration; queued scans remain local')
                previous_state = 'unconfigured'
                if args.once:
                    return 2
            else:
                uploader = Uploader('/scans', '/state', settings['destination'],
                                    '/usr/local/bin/rclone', '/config/rclone.conf', stopping=stopping.is_set)
                uploader.run_once()
                if previous_state != 'ready':
                    LOG.info('uploader ready; checking the queue every 60 seconds')
                previous_state = 'ready'
                if args.once:
                    return 0
        except Exception:
            LOG.exception('upload incomplete; keeping queued scans for retry')
            previous_state = 'failed'
            if args.once:
                return 1
        stopping.wait(60)
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
