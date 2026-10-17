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
  that can reach UDP 54925 can trigger a scan of whatever is on the glass.
