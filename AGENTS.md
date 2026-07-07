# AGENTS.md

## Project overview
token-profile is a Go CLI (`spf13/cobra`) that shells out to
[agentsview](https://github.com/kenn-io/agentsview) to resolve local AI
coding agent usage, merges per-machine snapshots across machines via git,
and renders a dashboard card into a target repo's README between
`<!-- token-profile:start/end -->` markers. The card is a light/dark SVG
image by default (`internal/render`, built with `ajstarks/svgo`; files land
in the target's `.token-profile/card-{light,dark}.svg` and are wired up via
`<picture>`), or an ASCII block — selected by `renderMode` in config.

## Dev environment setup
Go 1.26+. `go mod download` to fetch dependencies. The `agentsview` binary
must be on `PATH` for real (non-fixture) runs — see its own install docs.

## Build
`go build ./...`; `go build -o token-profile ./cmd/token-profile` for a
standalone binary. Releases are built via GoReleaser (`.goreleaser.yml`).

## Testing instructions
`go test ./...` (add `-race` when touching concurrency-sensitive code —
`internal/machineid`, `internal/gitops`, `internal/cli`'s run-lock). Follow
strict TDD: red → green → refactor. Package tests exercise real local `git`
fixtures (bare repo + clone) rather than mocking git; `internal/agentsview`
keeps real captured agentsview fixtures under `testdata/`, and
`internal/render` locks card output with golden fixtures (`.golden` for
ASCII, `.golden.svg` for the SVG variants).

## Code style guidelines
- Load the `modern-go-guidelines:use-modern-go` skill before non-trivial Go changes for the current guideline set.
- **Self-explanatory code first; comments are a last resort.** A comment may state only a design
  constraint, invariant, or non-obvious *why* — never narrate what the code does or restate the diff.

## PR / commit guidelines
- Use Conventional Commits (see `agd:conventional-commits` skill): `<type>[optional scope]: <description>`.
- `gofmt -l .` (must be empty) and `go vet ./...` (must be clean) before committing.

## Security considerations
Machine identity (`~/.token-profile/machine-id`) and config
(`~/.token-profile/config.json`) are local, unauthenticated files; a cached
machine-id's content is validated (32 lowercase hex chars) before use in any
file path, since it's untrusted input from the adopter's own disk. No
secrets are read, stored, or transmitted by this tool — `agentsview` and
`git` handle their own credentials independently.
