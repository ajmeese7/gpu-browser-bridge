# Contributing

Thanks for your interest in gpu-browser-bridge! This project is small and focused, so contributions that stay within the existing scope are most welcome.

## Getting started

1. Fork and clone the repo.
2. Install [Go 1.26+](https://go.dev/dl/).
3. Build and test:

```bash
go build ./...
go test ./...
go vet ./...
```

The bridge itself only runs on Windows (it needs a GPU-backed Chrome), but the CLI and most tests build on any OS.

## Submitting changes

- Open an issue first for anything non-trivial so we can discuss the approach.
- Keep PRs focused — one logical change per PR.
- Make sure `go test ./...` and `go vet ./...` pass.
- Follow the existing code style (gofmt, no unnecessary abstractions).

## Releases

Releases are automated via GitHub Actions. To cut a new release:

```bash
git tag v1.0.0
git push origin v1.0.0
```

The workflow cross-compiles binaries for all supported platforms (Windows amd64, Linux amd64/arm64, macOS amd64/arm64) and publishes them as GitHub Release assets with auto-generated notes.

## Reporting bugs

Open a GitHub issue with:
- What you expected to happen
- What actually happened
- OS, Go version, Chrome version
- Relevant logs (from `%LOCALAPPDATA%\gpu-browser-bridge\bridge.log`)

## Security issues

If you find a security vulnerability, please email aaron@meese.dev instead of opening a public issue.
