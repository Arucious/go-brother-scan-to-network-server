# Contributing

Thanks for helping! Bug reports with printer details, compatibility reports
and pull requests are all welcome.

## Development

Requirements: Go (see `go.mod`), Docker for the image and integration tests,
and optionally [golangci-lint](https://golangci-lint.run/) v2.

```sh
make help                 # list targets
make lint test            # gofmt, go vet, unit tests with -race
golangci-lint run         # the linters CI runs
make run                  # build and run with ./config.yaml
```

Destination code (SMB, FTP, S3) has integration tests against real servers in
Docker:

```sh
make integration-servers integration-test integration-stop
```

## Protocol work

The Brother protocols are reverse engineered; the README's *How it works*
section and the package docs in `internal/brother` and `internal/brscan` list
what is known. When adding support for another model or mode, record the raw
stream with `BRSCAN_CAPTURE_DIR=/some/dir` and add a test that replays the
relevant part, like the existing ones in `internal/brscan/brscan_test.go`.

## Pull requests

- Keep changes focused and include tests.
- Use [Conventional Commits](https://www.conventionalcommits.org/) for commit
  messages (`feat:`, `fix:`, `docs:` …); the release notes are generated from them.
- Update `README.md` and `config.yaml.example` when options change.

## Releasing

Push a `vX.Y.Z` tag. The release workflow runs the tests, publishes binaries
and a GitHub release (GoReleaser) and multi-arch images to Docker Hub and
GHCR. It needs the `DOCKERHUB_USERNAME` and `DOCKERHUB_TOKEN` repository
secrets; pushes to `main` publish the `edge` image.
