# AGENTS.md

## Token Profile
A token usage to asciiart dashboard CLI, aims to set in github profile README(in `<username>/<username>` repo).

## Dev environment setup
_TODO: fill in once a language/framework is chosen (e.g. `npm install`, `poetry install`, `go mod tidy`)._

## Build
_TODO: add the build command once one exists._

## Testing instructions
_TODO: add the test command once a test framework is set up. Follow strict TDD: red → green → refactor._

## Code style guidelines
- **Use strict TDD pattern**, red → green → refactor. 
- **Modern Go**: load skill `modern-go-guidelines:use-modern-go` before coding.
- **Self-explanatory code first; comments are a last resort.** Carry intent in names, types, and structure. A comment may state only a design constraint, an invariant, or a non-obvious *why* (tradeoff / gotcha) a competent reader cannot infer. Never narrate what the code does, restate the next line, restate the diff, or document your own reasoning history. Rationale *about the change* belongs in the commit message, not the file. Match the surrounding comment density.

## PR / commit guidelines
- Use Conventional Commits (see `agd:conventional-commits` skill);
