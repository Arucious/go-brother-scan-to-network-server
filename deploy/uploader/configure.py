#!/usr/bin/env python3
"""Prompt locally for NAS access and enable the uploader after a verified probe."""
from pathlib import Path
import configparser
import getpass
import hashlib
import io
import json
import os
import secrets
import subprocess
import tempfile

base = Path.home() / 'docker/brother-scan/uploader'
config_dir = base / 'config'
settings_path = config_dir / 'settings.json'
rclone = Path.home() / '.local/bin/rclone'
os.umask(0o077)
config_dir.mkdir(mode=0o700, exist_ok=True)
if settings_path.exists() and json.loads(settings_path.read_text()).get('destination'):
    raise SystemExit('Already configured. Stop scan-uploader before deliberately changing its destination.')

def prompt(label, default):
    return input(f'{label} [{default}]: ').strip() or default

host = prompt('NAS hostname or IP', '')
username = prompt('NAS account', '')
share = prompt('SMB share name', 'Scans')
subfolder = prompt('Subfolder inside that share', 'Brother')
domain = prompt('SMB domain', 'WORKGROUP')
if any(c in host for c in '/\\\r\n') or any(c in share for c in '/\\:\r\n'):
    raise SystemExit('Invalid host or share name')
if any(p in ('', '.', '..') for p in subfolder.split('/')) or '\\' in subfolder or ':' in subfolder:
    raise SystemExit('Subfolder must be a relative path such as Brother or Inbox/Brother')
password = getpass.getpass('NAS password (not echoed or saved in shell history): ')
if not password or '\n' in password or '\r' in password:
    raise SystemExit('A nonempty, single-line password is required')
obscured = subprocess.run([str(rclone), 'obscure', '-'], input=password + '\n',
                          text=True, capture_output=True, check=True).stdout.strip()
password = None
config = configparser.ConfigParser(interpolation=None)
config['scan-nas'] = {'type': 'smb', 'host': host, 'user': username,
                      'pass': obscured, 'domain': domain, 'port': '445'}
text = io.StringIO()
config.write(text)
fd, temporary = tempfile.mkstemp(prefix='.configure-', dir=config_dir)
try:
    with os.fdopen(fd, 'w') as stream:
        stream.write(text.getvalue())
        stream.flush()
        os.fsync(stream.fileno())
    command = [str(rclone), '--config', temporary]
    options = ['--retries', '1', '--low-level-retries', '2', '--contimeout', '10s', '--timeout', '30s']
    destination = 'scan-nas:' + share + '/' + subfolder
    subprocess.run(command + ['mkdir', destination] + options, check=True, timeout=90)
    probe_remote = destination + '/.brother-upload-check-' + secrets.token_hex(12)
    probe = secrets.token_bytes(64)
    with tempfile.NamedTemporaryFile(dir=base) as source:
        source.write(probe)
        source.flush()
        subprocess.run(command + ['copyto', source.name, probe_remote, '--immutable'] + options,
                       check=True, timeout=90)
        downloaded = subprocess.run(command + ['cat', probe_remote] + options,
                                    check=True, timeout=90, capture_output=True).stdout
        if hashlib.sha256(downloaded).digest() != hashlib.sha256(probe).digest():
            raise SystemExit('NAS read-back verification failed; uploader remains unconfigured')
        subprocess.run(command + ['deletefile', probe_remote] + options, check=True, timeout=90)
    os.replace(temporary, config_dir / 'rclone.conf')
    with tempfile.NamedTemporaryFile(mode='w', dir=config_dir, delete=False) as stream:
        json.dump({'destination': destination}, stream)
        stream.write('\n')
        stream.flush()
        os.fsync(stream.fileno())
        new_settings = stream.name
    os.replace(new_settings, settings_path)
    print('NAS write and SHA-256 read-back checks passed.')
    print('Enabled destination:', destination)
    print('The running uploader will pick this up on its next check (within 60 seconds).')
finally:
    if os.path.exists(temporary):
        os.unlink(temporary)
