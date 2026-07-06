# AGENTS.md

## Project overview
token-profile is a Go CLI (`spf13/cobra`) that shells out to
[agentsview](https://github.com/kenn-io/agentsview) to resolve local AI
coding agent usage, merges per-machine snapshots across machines via git,
and renders an ASCII dashboard card into a target repo's README between
`<!-- token-profile:start/end -->` markers. See `README.md` for the
adopter-facing docs and `docs/plans/2026-07-03-001-feat-github-token-profile-plan.md`
for the implementation plan.

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
keeps real captured agentsview fixtures under `testdata/`.

## Code style guidelines
`gofmt -l .` (must be empty) and `go vet ./...` (must be clean) before
committing. Use modern Go 1.26 idioms throughout: `any`, `slices`/`maps`/`cmp`,
`errors.Is`/`errors.AsType[T]`, `omitzero` json tags, `for i := range n`,
`min`/`max`, `new(val)`, `t.Context()` in tests. Load the
`modern-go-guidelines:use-modern-go` skill before non-trivial Go changes for
the current guideline set.
- **Self-explanatory code first; comments are a last resort.** Carry intent in names, types, and structure. A comment may state only a design constraint, an invariant, or a non-obvious *why* (tradeoff / gotcha) a competent reader cannot infer. Never narrate what the code does, restate the next line, restate the diff, or document your own reasoning history. Rationale *about the change* belongs in the commit message, not the file. Match the surrounding comment density.

## PR / commit guidelines
- Use Conventional Commits (see `agd:conventional-commits` skill): `<type>[optional scope]: <description>`.
- Common types: `feat`, `fix`, `docs`, `refactor`, `test`, `chore`.

## Security considerations
Machine identity (`~/.token-profile/machine-id`) and config
(`~/.token-profile/config.json`) are local, unauthenticated files; a cached
machine-id's content is validated (32 lowercase hex chars) before use in any
file path, since it's untrusted input from the adopter's own disk. No
secrets are read, stored, or transmitted by this tool — `agentsview` and
`git` handle their own credentials independently.
