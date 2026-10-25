Verified scan uploads

upload.py verifies completed scans against an rclone destination, saves a receipt,
and archives the local original under scans/uploaded/. Failed transfers stay
queued. Files at the destination with different contents are not overwritten.

Install a reviewed rclone binary as uploader/rclone before building the image.
The setup utility expects rclone at ~/.local/bin/rclone and a deployment at
~/docker/brother-scan. Adapt these paths to your installation.

Run configure.py interactively on the deployment host to enter your NAS host,
account and password. A restricted NAS account only needs access to the scan
inbox. The password is passed to rclone obscure through stdin. Obscuring is
reversible: protect the config directory and all backups as credentials.
Configuration is enabled only after a write/read-back/delete probe succeeds.

From the deployment directory:
  docker compose -f compose.yaml -f compose.upload.yaml up -d --build scan-uploader
  docker compose -f compose.yaml -f compose.upload.yaml logs --tail 50 scan-uploader

The scanner local destination should use path: /scans and archive_path: uploaded.
Keep the queue and archive on the same filesystem. There is no automatic expiry
for archived copies or receipts. Preserve both when migrating.

Tests with an installed rclone binary:
  RCLONE_TEST_BINARY=/path/to/rclone python3 -m unittest -v test_upload.py

Keep installation-specific addresses, accounts, SSH commands, operational notes,
.env files, rclone.conf, settings, and state in private storage outside this repo.
