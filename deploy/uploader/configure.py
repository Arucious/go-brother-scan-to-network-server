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
import sys
import tempfile

base = Path.home() / 'docker/brother-scan/uploader'
config_dir = base / 'config'
settings_path = config_dir / 'settings.json'
rclone = Path.home() / '.local/bin/rclone'
os.umask(0o077)
config_dir.mkdir(mode=0o700, exist_ok=True)
if settings_path.exists() and json.loads(settings_path.read_text()).get('destination'):
    raise SystemExit('Already configured. Stop scan-uploader before deliberately changing its destination.')

provided = None
if sys.argv[1:] == ['--stdin-json']:
    sys.stdin.reconfigure(encoding='utf-8')
    payload = sys.stdin.read(65537)
    if len(payload) > 65536:
        raise SystemExit('Connection input is too large')
    provided = json.loads(payload)
    payload = None
    allowed = {'host', 'username', 'share', 'subfolder', 'domain', 'password'}
    if not isinstance(provided, dict) or set(provided) - allowed:
        raise SystemExit('Invalid connection fields')
    if any(not isinstance(value, str) for value in provided.values()):
        raise SystemExit('Connection fields must be strings')
elif sys.argv[1:]:
    raise SystemExit('Usage: configure.py [--stdin-json]')

def prompt(key, label, default):
    if provided is not None:
        return provided.get(key, default).strip()
    return input(f'{label} [{default}]: ').strip() or default

host = prompt('host', 'NAS hostname or IP', '')
username = prompt('username', 'NAS account', '')
share = prompt('share', 'SMB share name', 'Scans')
subfolder = prompt('subfolder', 'Subfolder inside that share', 'Brother')
domain = prompt('domain', 'SMB domain', 'WORKGROUP')
if any(not value or any(c in value for c in '\r\n\0') for value in (host, username, share, subfolder, domain)):
    raise SystemExit('Connection fields must be nonempty single-line values')
if any(c in host for c in '/\\\r\n') or any(c in share for c in '/\\:\r\n'):
    raise SystemExit('Invalid host or share name')
if any(p in ('', '.', '..') for p in subfolder.split('/')) or '\\' in subfolder or ':' in subfolder:
    raise SystemExit('Subfolder must be a relative path such as Brother or Inbox/Brother')
password = provided.pop('password', '') if provided is not None else getpass.getpass('NAS password (not echoed or saved in shell history): ')
provided = None
if not password or any(c in password for c in '\r\n\0'):
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
