package cli

import (
	"html"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Christophe1997/token-profile/internal/agentsview"
	"github.com/Christophe1997/token-profile/internal/config"
	"github.com/Christophe1997/token-profile/internal/readme"
	"github.com/Christophe1997/token-profile/internal/render"
	"github.com/Christophe1997/token-profile/internal/snapshot"
	"github.com/Christophe1997/token-profile/internal/summary"
)

// TestWriteCardFile_MkdirAllFailure covers writeCardFile's first error
// branch: relPath's parent directory can't be created because a regular
// file already occupies that path, so os.MkdirAll fails.
func TestWriteCardFile_MkdirAllFailure(t *testing.T) {
	repoDir := t.TempDir()
	blocker := filepath.Join(repoDir, ".token-profile")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", blocker, err)
	}

	err := writeCardFile(repoDir, svgLightRelPath, "<svg></svg>")
	if err == nil {
		t.Fatal("writeCardFile() error = nil, want an error when the parent path is a file")
	}
	if !strings.Contains(err.Error(), "creating directory for") {
		t.Errorf("writeCardFile() error = %q, want it to mention the directory-creation failure", err.Error())
	}
}

// TestWriteCardFile_WriteFileFailure covers writeCardFile's second error
// branch: relPath itself is pre-occupied by a directory, so os.WriteFile
// fails to write the card content to it.
func TestWriteCardFile_WriteFileFailure(t *testing.T) {
	repoDir := t.TempDir()
	blocker := filepath.Join(repoDir, filepath.FromSlash(svgLightRelPath))
	if err := os.MkdirAll(blocker, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s) error = %v", blocker, err)
	}

	err := writeCardFile(repoDir, svgLightRelPath, "<svg></svg>")
	if err == nil {
		t.Fatal("writeCardFile() error = nil, want an error when relPath is a directory")
	}
	if !strings.Contains(err.Error(), "writing "+svgLightRelPath) {
		t.Errorf("writeCardFile() error = %q, want it to mention the write failure", err.Error())
	}
}

// TestRun_OutOfEnumRenderMode_FilesAndRenderBranchAgree covers a RunDeps
// built with a RenderMode outside {svg, ascii} — unreachable via the normal
// config.Load path (Validate rejects it) but constructible directly, as
// every test in this package already does. mergeRenderInject's switch
// treats anything other than exactly RenderModeASCII as the SVG branch, so
// the git-add file list must use the identical predicate (code review F8) —
// otherwise the SVG files get rendered and written to disk but never
// committed, leaving them untracked after an apparently-successful run.
func TestRun_OutOfEnumRenderMode_FilesAndRenderBranchAgree(t *testing.T) {
	remote := initBareRemote(t)
	seedRemote(t, remote, markedReadme)

	work := cloneWorkdir(t, remote, "svg-out-of-enum")
	bin := fakeAgentsviewBinary(t, "claude-code", "claude-sonnet-5", "2026-06-20", 1000, 1.5)

	deps := RunDeps{
		Config:    config.Config{Breakdown: config.BreakdownPerModel, RenderMode: config.RenderMode("bogus")},
		Client:    &agentsview.Client{BinaryName: bin},
		MachineID: "machine-svg-bogus",
		Now:       time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC),
		RepoDir:   work,
	}

	if err := Run(t.Context(), deps); err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}

	for _, relPath := range []string{svgLightRelPath, svgDarkRelPath} {
		if _, err := os.Stat(filepath.Join(work, filepath.FromSlash(relPath))); err != nil {
			t.Fatalf("Stat(%s) error = %v, want the SVG file rendered to disk", relPath, err)
		}
	}

	tracked := runGitT(t, work, "ls-tree", "-r", "--name-only", "HEAD")
	for _, want := range []string{svgLightRelPath, svgDarkRelPath} {
		if !strings.Contains(tracked, want) {
			t.Errorf("git ls-tree HEAD = %q, want it to include %q — rendered SVG files must be committed even for an out-of-enum RenderMode", tracked, want)
		}
	}
	status := runGitT(t, work, "status", "--porcelain")
	if strings.TrimSpace(status) != "" {
		t.Errorf("git status --porcelain = %q, want a clean working tree (no untracked SVG files left behind)", status)
	}
}

// TestRun_SVGMode_DefaultInjectsPictureMarkupAndWritesFiles covers AE1: an
// adopter who has not set RenderMode gets the new SVG card automatically —
// both light and dark SVG files land on disk under deps.RepoDir, and the
// README is injected with <picture> markup referencing them instead of the
// ASCII fence.
func TestRun_SVGMode_DefaultInjectsPictureMarkupAndWritesFiles(t *testing.T) {
	remote := initBareRemote(t)
	seedRemote(t, remote, markedReadme)

	work := cloneWorkdir(t, remote, "svg-default")
	bin := fakeAgentsviewBinary(t, "claude-code", "claude-sonnet-5", "2026-06-20", 1000, 1.5)

	deps := RunDeps{
		// RenderMode deliberately left unset — AE1's premise.
		Config:    config.Config{Breakdown: config.BreakdownPerModel},
		Client:    &agentsview.Client{BinaryName: bin},
		MachineID: "machine-svg",
		Now:       time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC),
		RepoDir:   work,
	}

	if err := Run(t.Context(), deps); err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}

	verify := cloneWorkdir(t, remote, "verify-svg-default")

	for _, relPath := range []string{svgLightRelPath, svgDarkRelPath} {
		content, err := os.ReadFile(filepath.Join(verify, filepath.FromSlash(relPath)))
		if err != nil {
			t.Fatalf("ReadFile(%s) error = %v, want the SVG file present", relPath, err)
		}
		if !strings.Contains(string(content), "<svg") {
			t.Errorf("%s content = %q, want it to contain an <svg> element", relPath, content)
		}
	}

	readmeBytes, err := os.ReadFile(filepath.Join(verify, "README.md"))
	if err != nil {
		t.Fatalf("ReadFile(README.md) error = %v", err)
	}
	got := string(readmeBytes)
	if !strings.Contains(got, "<details open>") {
		t.Errorf("README missing <details open> — KTD6 wants the SVG card expanded by default:\n%s", got)
	}
	if !strings.Contains(got, "<picture>") {
		t.Errorf("README missing <picture> markup:\n%s", got)
	}
	if !strings.Contains(got, `media="(prefers-color-scheme: dark)"`) {
		t.Errorf("README missing dark-mode media query:\n%s", got)
	}
	if !strings.Contains(got, `srcset="`+svgDarkRelPath+`"`) {
		t.Errorf("README <source> missing dark SVG path %q:\n%s", svgDarkRelPath, got)
	}
	if !strings.Contains(got, `src="`+svgLightRelPath+`"`) {
		t.Errorf("README <img> missing light SVG path %q:\n%s", svgLightRelPath, got)
	}
	if strings.Contains(got, "```") {
		t.Errorf("README unexpectedly still contains an ASCII fence under the SVG default:\n%s", got)
	}
}

// TestRun_SVGMode_FilesTrackedInSameCommitAsReadme covers the Integration
// scenario: the two SVG files must actually be committed alongside the
// README update, not left as untracked or uncommitted working-tree files —
// gitops.Publish only pushes what run()'s files slice names explicitly.
func TestRun_SVGMode_FilesTrackedInSameCommitAsReadme(t *testing.T) {
	remote := initBareRemote(t)
	seedRemote(t, remote, markedReadme)

	work := cloneWorkdir(t, remote, "svg-tracked")
	bin := fakeAgentsviewBinary(t, "claude-code", "claude-sonnet-5", "2026-06-20", 1000, 1.5)

	deps := RunDeps{
		Config:    config.Config{Breakdown: config.BreakdownPerModel},
		Client:    &agentsview.Client{BinaryName: bin},
		MachineID: "machine-svg",
		Now:       time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC),
		RepoDir:   work,
	}

	if err := Run(t.Context(), deps); err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}

	tracked := runGitT(t, work, "ls-tree", "-r", "--name-only", "HEAD")
	for _, want := range []string{svgLightRelPath, svgDarkRelPath, readmeFile} {
		if !strings.Contains(tracked, want) {
			t.Errorf("git ls-tree HEAD = %q, want it to include %q", tracked, want)
		}
	}

	status := runGitT(t, work, "status", "--porcelain")
	if strings.TrimSpace(status) != "" {
		t.Errorf("git status --porcelain = %q, want a clean working tree (SVG files committed, not left untracked)", status)
	}

	verify := cloneWorkdir(t, remote, "verify-svg-tracked")
	for _, relPath := range []string{svgLightRelPath, svgDarkRelPath} {
		if _, err := os.Stat(filepath.Join(verify, filepath.FromSlash(relPath))); err != nil {
			t.Errorf("Stat(%s) in a fresh clone error = %v, want the file present (proves it was pushed, not just committed locally)", relPath, err)
		}
	}
}

// TestRun_ASCIIMode_MatchesPriorRenderExactly covers AE2: an explicit ASCII
// RenderMode must inject byte-for-byte the same content mergeRenderInject's
// ASCII branch always has — computed independently here via render.Render,
// fenceCard, and collapsible directly from the same fixture Run() resolves
// — proving this unit's branch left the ASCII wiring itself unchanged, not
// just its outward shape (TestRun_EndToEnd_CardIsFencedAndCollapsible
// covers that).
func TestRun_ASCIIMode_MatchesPriorRenderExactly(t *testing.T) {
	remote := initBareRemote(t)
	seedRemote(t, remote, markedReadme)

	work := cloneWorkdir(t, remote, "ascii-parity")
	bin := fakeAgentsviewBinary(t, "claude-code", "claude-sonnet-5", "2026-06-20", 1000, 1.5)
	now := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)

	deps := RunDeps{
		Config:    config.Config{Breakdown: config.BreakdownPerModel, RenderMode: config.RenderModeASCII},
		Client:    &agentsview.Client{BinaryName: bin},
		MachineID: "machine-ascii",
		Now:       now,
		RepoDir:   work,
	}
	if err := Run(t.Context(), deps); err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}

	verify := cloneWorkdir(t, remote, "verify-ascii-parity")
	readmeBytes, err := os.ReadFile(filepath.Join(verify, "README.md"))
	if err != nil {
		t.Fatalf("ReadFile(README.md) error = %v", err)
	}
	got := string(readmeBytes)

	startIdx := strings.Index(got, readme.StartMarker)
	endIdx := strings.Index(got, readme.EndMarker)
	if startIdx == -1 || endIdx == -1 {
		t.Fatalf("README missing token-profile markers:\n%s", got)
	}
	gotInjected := strings.TrimSpace(got[startIdx+len(readme.StartMarker) : endIdx])

	ds := snapshot.MergedDataset{Rows: []snapshot.Row{
		{Date: "2026-06-20", Agent: "claude-code", Model: "claude-sonnet-5", Tokens: 1000, Cost: 1.5},
	}}
	sum := summary.Compute(ds, now, config.DefaultTrailingWindow)
	wantCard := render.Render(ds, sum, config.BreakdownPerModel, config.DefaultBreakdownLimit, now)
	wantSummaryText := render.CardTitle + " — " + render.Headline(sum)
	wantInjected := collapsible(wantSummaryText, fenceCard(wantCard), false)

	if gotInjected != wantInjected {
		t.Errorf("injected ASCII content =\n%s\nwant exactly\n%s", gotInjected, wantInjected)
	}
}

// TestRun_SVGMode_SecondRunOverwritesSameFiles covers the edge case: two
// SVG-mode runs against the same repo must always resolve to the exact
// same two file paths, with the second run's content replacing the
// first's rather than accumulating extra files.
func TestRun_SVGMode_SecondRunOverwritesSameFiles(t *testing.T) {
	remote := initBareRemote(t)
	seedRemote(t, remote, markedReadme)
	work := cloneWorkdir(t, remote, "svg-rerun")

	firstBin := fakeAgentsviewBinary(t, "claude-code", "claude-sonnet-5", "2026-06-20", 1000, 1.5)
	deps := RunDeps{
		Config:    config.Config{Breakdown: config.BreakdownPerModel},
		Client:    &agentsview.Client{BinaryName: firstBin},
		MachineID: "machine-svg-rerun",
		Now:       time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC),
		RepoDir:   work,
	}
	if err := Run(t.Context(), deps); err != nil {
		t.Fatalf("first Run() error = %v, want nil", err)
	}

	firstLight, err := os.ReadFile(filepath.Join(work, filepath.FromSlash(svgLightRelPath)))
	if err != nil {
		t.Fatalf("ReadFile(%s) after first run error = %v", svgLightRelPath, err)
	}

	secondBin := fakeAgentsviewBinary(t, "claude-code", "claude-sonnet-5", "2026-06-21", 2000, 3.0)
	deps.Client = &agentsview.Client{BinaryName: secondBin}
	deps.Now = time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)
	if err := Run(t.Context(), deps); err != nil {
		t.Fatalf("second Run() error = %v, want nil", err)
	}

	secondLight, err := os.ReadFile(filepath.Join(work, filepath.FromSlash(svgLightRelPath)))
	if err != nil {
		t.Fatalf("ReadFile(%s) after second run error = %v", svgLightRelPath, err)
	}
	if string(firstLight) == string(secondLight) {
		t.Errorf("%s content unchanged across runs with different data, want it overwritten with the second run's totals", svgLightRelPath)
	}

	matches, err := filepath.Glob(filepath.Join(work, ".token-profile", "card-*.svg"))
	if err != nil {
		t.Fatalf("Glob() error = %v", err)
	}
	if len(matches) != 2 {
		t.Errorf("card-*.svg files on disk = %v, want exactly 2 (no accumulation across runs)", matches)
	}

	verify := cloneWorkdir(t, remote, "verify-svg-rerun")
	tracked := runGitT(t, verify, "ls-tree", "-r", "--name-only", "HEAD")
	if svgCount := strings.Count(tracked, ".token-profile/card-"); svgCount != 2 {
		t.Errorf("tracked card-*.svg entries in HEAD = %d, want exactly 2:\n%s", svgCount, tracked)
	}
}

// extractAttr returns attr's value out of a raw HTML/markdown blob (e.g.
// `alt="Token Profile — last 1 day..."`), failing the test if attr isn't
// present. It doesn't handle escaped quotes inside the value — not needed
// here, since html.EscapeString never emits a literal `"` (it renders as
// `&#34;`).
func extractAttr(t *testing.T, blob, attr string) string {
	t.Helper()
	marker := attr + `="`
	idx := strings.Index(blob, marker)
	if idx == -1 {
		t.Fatalf("missing %s=\"...\" attribute in:\n%s", attr, blob)
	}
	start := idx + len(marker)
	end := strings.Index(blob[start:], `"`)
	if end == -1 {
		t.Fatalf("unterminated %s attribute in:\n%s", attr, blob)
	}
	return blob[start : start+end]
}

// TestRun_SVGMode_AltTextContainsHeadlineStats covers AE4: the injected
// <picture>'s <img alt="..."> must carry the actual headline
// tokens/cost/streak summary render.AltText produces for this run's
// fixture, computed independently here the same way
// TestRun_ASCIIMode_MatchesPriorRenderExactly cross-checks the ASCII
// path — not just that the picture/source markup exists (already covered
// by TestRun_SVGMode_DefaultInjectsPictureMarkupAndWritesFiles), so a
// screen reader or non-rendering context actually gets the core numbers.
func TestRun_SVGMode_AltTextContainsHeadlineStats(t *testing.T) {
	remote := initBareRemote(t)
	seedRemote(t, remote, markedReadme)

	work := cloneWorkdir(t, remote, "svg-alt")
	bin := fakeAgentsviewBinary(t, "claude-code", "claude-sonnet-5", "2026-06-20", 1000, 1.5)
	now := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)

	deps := RunDeps{
		// RenderMode deliberately left unset — same SVG-default premise as
		// TestRun_SVGMode_DefaultInjectsPictureMarkupAndWritesFiles.
		Config:    config.Config{Breakdown: config.BreakdownPerModel},
		Client:    &agentsview.Client{BinaryName: bin},
		MachineID: "machine-svg-alt",
		Now:       now,
		RepoDir:   work,
	}
	if err := Run(t.Context(), deps); err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}

	verify := cloneWorkdir(t, remote, "verify-svg-alt")
	readmeBytes, err := os.ReadFile(filepath.Join(verify, "README.md"))
	if err != nil {
		t.Fatalf("ReadFile(README.md) error = %v", err)
	}
	gotAlt := extractAttr(t, string(readmeBytes), "alt")

	// Same single-run fixture Run() resolved: the merged dataset (there's
	// no other machine or prior snapshot) and the current window are
	// identical, so this reproduces exactly what svgCardBody computed.
	ds := snapshot.MergedDataset{Rows: []snapshot.Row{
		{Date: "2026-06-20", Agent: "claude-code", Model: "claude-sonnet-5", Tokens: 1000, Cost: 1.5},
	}}
	sum := summary.Compute(ds, now, config.DefaultTrailingWindow)
	wantAlt := html.EscapeString(render.AltText(ds, sum))

	if gotAlt != wantAlt {
		t.Errorf("<img alt> = %q, want exactly %q (render.AltText for the same fixture)", gotAlt, wantAlt)
	}
	for _, want := range []string{"1,000", "$1.50", "Streak"} {
		if !strings.Contains(gotAlt, want) {
			t.Errorf("<img alt> = %q, want it to contain headline substring %q", gotAlt, want)
		}
	}
}

// TestRun_SVGMode_AttributionOutsidePictureBlock covers R2 on the SVG
// render path specifically: TestRun_EndToEnd_CardIsFencedAndCollapsible
// already proves the attribution line sits after the ASCII card's closing
// fence, but nothing previously proved the same ordering for svgCardBody's
// <picture> markup — this is that SVG-branch counterpart.
func TestRun_SVGMode_AttributionOutsidePictureBlock(t *testing.T) {
	remote := initBareRemote(t)
	seedRemote(t, remote, markedReadme)

	work := cloneWorkdir(t, remote, "svg-order")
	bin := fakeAgentsviewBinary(t, "claude-code", "claude-sonnet-5", "2026-06-20", 1000, 1.5)

	deps := RunDeps{
		// RenderMode deliberately left unset — same SVG-default premise as
		// TestRun_SVGMode_DefaultInjectsPictureMarkupAndWritesFiles.
		Config:    config.Config{Breakdown: config.BreakdownPerModel},
		Client:    &agentsview.Client{BinaryName: bin},
		MachineID: "machine-svg-order",
		Now:       time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC),
		RepoDir:   work,
	}
	if err := Run(t.Context(), deps); err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}

	verify := cloneWorkdir(t, remote, "verify-svg-order")
	readmeBytes, err := os.ReadFile(filepath.Join(verify, "README.md"))
	if err != nil {
		t.Fatalf("ReadFile(README.md) error = %v", err)
	}
	got := string(readmeBytes)

	startIdx := strings.Index(got, readme.StartMarker)
	endIdx := strings.Index(got, readme.EndMarker)
	if startIdx == -1 || endIdx == -1 || endIdx < startIdx {
		t.Fatalf("README missing token-profile markers:\n%s", got)
	}
	injected := strings.TrimSpace(got[startIdx+len(readme.StartMarker) : endIdx])

	summaryEnd := strings.Index(injected, "</summary>")
	pictureOpen := strings.Index(injected, "<picture>")
	pictureClose := strings.Index(injected, "</picture>")
	attribution := strings.Index(injected, render.GeneratedByLine())

	if summaryEnd == -1 || pictureOpen == -1 || pictureClose == -1 || attribution == -1 {
		t.Fatalf("injected content missing an expected part: summaryEnd=%d pictureOpen=%d pictureClose=%d attribution=%d\ninjected:\n%s",
			summaryEnd, pictureOpen, pictureClose, attribution, injected)
	}
	if !(summaryEnd < pictureOpen && pictureOpen < pictureClose && pictureClose < attribution) {
		t.Errorf("injected content out of order: summaryEnd=%d pictureOpen=%d pictureClose=%d attribution=%d\ninjected:\n%s",
			summaryEnd, pictureOpen, pictureClose, attribution, injected)
	}
}
