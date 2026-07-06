# Residual review findings — feat/token-profile-cli

Source: multi-persona code review (correctness, testing, maintainability,
project-standards, agent-native, learnings, security, reliability,
adversarial) of the token-profile implementation, run against
`docs/plans/2026-07-03-001-feat-github-token-profile-plan.md`. Base commit
for the reviewed diff: `7f34772a16b0510a6389dbb74fd083c09a9d001f`. Review
run artifacts: `/tmp/compound-engineering/ce-code-review/20260703-182208-2e0043a4/`.

All P0 and P1 findings from that review were fixed and verified (including
against the real `agentsview` binary) — see the commit history on this
branch. The 5 findings below are P2, judged non-blocking, and accepted as
known residuals rather than fixed now.

## 1. `agentsview.ExitError` and `gitops.ExitError` duplicate the same shape

**Severity:** P2 · **Confidence:** 75 · **Reviewer:** maintainability

Both types independently implement the same "label + stderr + `Unwrap`"
exec-error pattern (`internal/agentsview/client.go`, `internal/gitops/gitops.go:31`).
Not incorrect, just duplicated. A shared `execError` type in a small common
package would remove the duplication if it recurs a third time.

## 2. Git-fixture test helpers duplicated across `gitops` and `cli` tests

**Severity:** P2 · **Confidence:** 100 · **Reviewer:** maintainability

`initBareRemote`/`cloneWorkdir`/`writeFile`/`runGitT` are defined verbatim
in both `internal/gitops/gitops_test.go` and `internal/cli/run_test.go`
(`internal/cli/run_test.go:38`). Test-only duplication; extracting a shared
internal test-helper package would remove it if a third package needs the
same fixtures.

## 3. `snapshot.Merge` reports corrupted-file warnings via the global `log` package

**Severity:** P2 · **Confidence:** 75 · **Reviewer:** maintainability

`internal/snapshot/merge.go:61` uses `log.Printf` to warn about a skipped,
corrupted snapshot file rather than returning the warning to the caller.
Works correctly today (the corrupted file is still skipped, the merge still
succeeds), but a caller can't programmatically observe which files were
skipped without scraping the global log. Returning `[]error` (or similar)
alongside the successful `MergedDataset` would let `internal/cli` surface
this to the adopter instead of it only reaching stderr via the global logger.

## 4. Dashboard box measures width in Unicode code points, not display columns

**Severity:** P2 · **Confidence:** 100 · **Reviewer:** adversarial

`internal/render/render.go`'s `box()` uses `utf8.RuneCountInString` for
padding width. A CJK or emoji agent/model name (each such rune commonly
renders 2 terminal columns wide) would overflow the bordered box's right
edge, since the padding assumes 1 column per rune. Fixing this correctly
needs a display-width-aware measure (e.g.
`github.com/mattn/go-runewidth`'s `StringWidth`) — a new dependency, not
attempted here. Low real-world likelihood today (agentsview's own agent/model
identifiers observed in testing are all ASCII), but a real gap if that ever
changes.

## 5. Sustained push failures across separate scheduled runs accumulate unbounded unpushed local commits

**Severity:** P2 · **Confidence:** 75 · **Reviewer:** adversarial

Within a *single* `run`/`init` invocation, `gitops.Publish`'s retry loop
correctly amends one commit rather than stacking new ones (fixed in this
branch). But if push keeps failing across *separate* scheduled invocations
(e.g. the adopter's machine has no network for several scheduled cycles),
each invocation still creates a brand-new commit on top of the previous
unpushed one, since nothing checks for an already-existing unpushed
`token-profile` commit at the start of a run. Not destructive — no data is
lost, and the next successful push carries everything — but git history
accumulates one throwaway "refresh" commit per failed cycle until push
finally succeeds. A fix would need `Run` to detect and amend an existing
unpushed head commit (by conventional-commit-prefix match, or an
`is-ancestor`-style check against the upstream) before creating a new one.

---

Also noted but not tracked as findings above (soft-bucket
residual-risk/testing-gap items from the review, informational only):
asciigraph's behavior on extreme/negative token series near int64 bounds is
unverified; `init`'s scheduling entries have no jitter, so synchronized
multi-machine cron/launchd intervals make the push-retry path exercise more
often than an offset schedule would; embedded control characters (newlines,
ANSI escapes, RTL override) in an agent/model name aren't sanitized before
rendering; several unit-level test-coverage gaps exist in less-central
branches (`sinceDate`'s window>0 path, `schedulingEntryContent`'s two OS
branches, `fetchSessionListPage`'s error paths) that don't affect the
KTD-labeled invariants (all of which have direct test coverage).
