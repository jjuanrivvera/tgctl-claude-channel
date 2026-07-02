# Contributing

Thanks for your interest!

## Development

```sh
make verify   # gofmt + go vet + golangci-lint + tests (-race) + coverage floor
make hooks    # install the pre-commit hook (runs fmt/vet/tests before each commit)
make build    # -> bin/tgctl-claude-channel
```

Run `make verify` before pushing — CI runs the same gate, and the coverage floor (80%) is enforced.

## Principles

- **Keep the channel a thin wrapper over [`tgctl`](https://github.com/jjuanrivvera/tgctl).** A new Telegram capability should be a `tgctl` command the tool shells out to, not hand-rolled Bot-API code. That is the whole design: `tgctl` owns the API surface and the credential.
- **New behavior needs tests.** The security-critical logic (the access gate, the permission relay) should stay near 100%.
- **The gate is a pure function** — keep it that way so it's exhaustively testable.

## Releases

Releases are cut by pushing a `vX.Y.Z` tag; a GitHub Actions workflow runs [GoReleaser](https://goreleaser.com) to build cross-platform binaries and publish the release. Update `CHANGELOG.md` and the `version` in `main.go` and `.claude-plugin/plugin.json` before tagging.

Commit messages are plain and factual; conventional-commit prefixes are welcome but not required.
