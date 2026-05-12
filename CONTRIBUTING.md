# Contributing to statsig-to-ld

Thanks for your interest in contributing! This project lives in the
[LaunchDarkly Labs](https://github.com/launchdarkly-labs) organization, which
means it is **not officially supported by LaunchDarkly**. It is maintained on a
best-effort basis by the project maintainers and the community of users.

## Reporting bugs and requesting features

Please use [GitHub Issues](https://github.com/launchdarkly-labs/statsig-to-ld/issues)
to report bugs and request features. Before opening a new issue, please search
existing issues to avoid duplicates.

**Security issues should not be filed as GitHub issues.** See
[`SECURITY.md`](./SECURITY.md) for the disclosure process.

When filing a bug, include:

- What you ran (command, flags, version of the CLI).
- What you expected to happen.
- What actually happened, including any error output or migration report
  excerpts. **Redact API keys and other secrets** before posting.
- Your operating system and Go version (if you built from source).

## Development setup

You need [Go 1.24+](https://go.dev/dl/) to build from source.

```bash
git clone https://github.com/launchdarkly-labs/statsig-to-ld.git
cd statsig-to-ld
go build -o statsig-to-ld .
```

Run the test suite with:

```bash
go test ./...
```

The CI workflow in `.github/workflows/go.yml` runs the same checks on every PR.

## Submitting changes

1. Fork the repository and create a topic branch from `main`.
2. Make your changes. Keep commits focused and write clear commit messages.
3. Add or update tests where it makes sense. Run `go test ./...` and
   `go vet ./...` locally before opening the PR.
4. If your change is user-visible, add an entry under `## [Unreleased]` in
   [`CHANGELOG.md`](./CHANGELOG.md).
5. Open a pull request against `main` describing the change and the motivation.

For larger changes, please open an issue first to discuss the approach before
investing significant time in an implementation.

## Code of conduct

Be respectful and constructive in issues, pull requests, and reviews. We follow
the spirit of the [Contributor Covenant](https://www.contributor-covenant.org/).

## License

By contributing, you agree that your contributions will be licensed under the
[Apache License, Version 2.0](./LICENSE).
