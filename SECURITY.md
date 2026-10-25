# Security

## Reporting a vulnerability

Please report security issues privately through
[GitHub security advisories](https://github.com/aurimasniekis/go-brother-scan-to-network-server/security/advisories/new)
rather than public issues. You will get a response within a few days.

## Deployment notes

- The web UI can edit the configuration (including destination credentials)
  and start scans. Set `web.username` / `web.password` or keep it on a trusted
  network.
- Keep secrets out of the config file with `${ENV}` references.
- The printer protocols are unauthenticated LAN protocols by design: anything
  able to spoof the configured printer's source IP can trigger a scan of whatever
  is on the glass. Other source addresses are rejected before acknowledgement.
- Browser cross-origin writes are rejected. Invalid configuration fails closed
  for the web UI; repair the configuration on disk and restart the service.
- Use a dedicated scan directory. Local reads and writes are confined with
  `os.Root`, including symlink traversal; newly saved files use mode 0600.
- Run unprivileged with a read-only container root, no capabilities, memory and
  process limits, and only the config and scan directories mounted writable.
- Keep the dashboard on loopback behind SSH, or behind an authenticated TLS
  reverse proxy. Basic authentication alone does not encrypt HTTP traffic.
