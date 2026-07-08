package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"

	"charm.land/huh/v2"

	"github.com/Christophe1997/token-profile/internal/config"
)

// ErrWizardCancelled signals that RunWizard's trailing confirm group was
// declined -- the wizard's own cancellation signal. huh's accessible-mode
// Form.Run() discards each field's own RunAccessible error and
// unconditionally returns nil (see RunWizard's doc comment for the full
// rationale, KTD2), so no scripted accessible-mode input can ever produce
// huh.ErrUserAborted; callers must check for ErrWizardCancelled instead.
var ErrWizardCancelled = errors.New("setup wizard cancelled")

// githubUsernameRe matches GitHub's username shape (KTD11): alphanumeric or
// hyphen, no leading/trailing hyphen, 1-39 characters.
var githubUsernameRe = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?$`)

// wireFormIO applies accessible/input/output onto form — the
// WithAccessible/WithInput/WithOutput boilerplate shared by RunWizard and
// cleanup.go's confirmCleanup. Nil input/output are left as huh's own
// defaults (the real terminal), matching both callers' doc comments.
func wireFormIO(form *huh.Form, accessible bool, input io.Reader, output io.Writer) *huh.Form {
	form = form.WithAccessible(accessible)
	if input != nil {
		form = form.WithInput(input)
	}
	if output != nil {
		form = form.WithOutput(output)
	}
	return form
}

// WizardResult is the three fields R2's setup wizard collects. A later unit
// resolves these into an actual clone: RepoName and CloneProtocol build the
// remote URL (via profileRepoURL), LocalPath is where it's cloned to.
type WizardResult struct {
	// RepoName is the GitHub username/handle used to construct the
	// profile-repo clone URL via the existing username/username
	// convention (see profileRepoURL).
	RepoName string
	// CloneProtocol is the URL scheme to clone RepoName's profile repo
	// with.
	CloneProtocol config.CloneProtocol
	// LocalPath is where the profile repo should be cloned to locally.
	LocalPath string
}

// WizardDeps bundles RunWizard's dependencies as a struct -- Input and
// Output are same-shaped io values a positional signature would invite
// mixing up, mirroring bootstrapDeps/InitDeps's own rationale.
type WizardDeps struct {
	// GitUserName resolves the operator's global git user.name, used to
	// pre-fill RepoName/LocalPath. Injected so tests don't depend on the
	// real machine's git config, mirroring bootstrapDeps.GitUserName.
	GitUserName func(ctx context.Context) string

	// Accessible drives the underlying huh.Form in accessible (TTY-free)
	// mode. Production callers leave this false so the form renders
	// interactively; every test in this package sets it true (KTD2).
	Accessible bool

	// Input and Output are the form's IO streams. Nil defers to huh's own
	// defaults (the real terminal, in interactive mode); tests set both to
	// drive the form entirely TTY-free.
	Input  io.Reader
	Output io.Writer
}

// RunWizard collects the three fields R2's guided-init wizard needs -- a
// GitHub username/handle, clone protocol, and local clone path -- via a
// huh.Form (an input/select/input group plus a trailing confirm group),
// pre-filled using the same default-guessing logic as the narrow
// auto-clone shortcut it eventually replaces (gitGlobalUserName,
// validAutoCloneName, defaultStateFile; see bootstrapConfig).
//
// The trailing confirm group is the form's actual cancellation signal
// (KTD2, verified against huh v2.0.3's source): huh's accessible-mode
// Form.Run() (runAccessible in charm.land/huh/v2's form.go) discards each
// field's own RunAccessible error via a bare `_ =` and always returns nil,
// so no scripted-input sequence driven through accessible mode can ever
// produce huh.ErrUserAborted -- only a real interactive ctrl+c, handled by
// huh's non-accessible tea.Program path, can. RunWizard therefore reads the
// confirm field's own value after Form.Run() returns and treats a decline
// as cancellation. The huh.ErrUserAborted check below remains as a
// secondary, production-only safeguard for that real ctrl+c path; it is
// not (and cannot be) exercised by this package's accessible-mode tests,
// which is expected.
//
// RepoName is validated against GitHub's username shape (KTD11) after
// Form.Run() returns, rather than via the input field's own Validate: an
// accessible-mode submission of a blank line validates the *empty string*
// itself (not the pre-filled default it then falls back to per
// accessibility.PromptString's cmp.Or), so a field-level Validate would
// never catch a git user.name-guessed default like "John Smith" (a space
// isn't path-unsafe, so validAutoCloneName already accepts it as a
// pre-fill) sailing through unedited into a later unit's clone step as a
// broken URL.
func RunWizard(ctx context.Context, deps WizardDeps) (WizardResult, error) {
	name := deps.GitUserName(ctx)

	var repoDefault, localPathDefault string
	if validAutoCloneName(name) {
		repoDefault = name
		localPathDefault = defaultStateFile(filepath.Join("repos", name))
	}

	repo := repoDefault
	protocol := config.CloneProtocolHTTPS
	localPath := localPathDefault
	var confirmed bool

	form := huh.NewForm(
		huh.NewGroup(
			huh.NewInput().
				Title("GitHub username/handle").
				Description("Used to guess your profile repo's clone URL (username/username convention).").
				Value(&repo),
			huh.NewSelect[config.CloneProtocol]().
				Title("Clone protocol").
				Options(
					huh.NewOption(string(config.CloneProtocolHTTPS), config.CloneProtocolHTTPS),
					huh.NewOption(string(config.CloneProtocolSSH), config.CloneProtocolSSH),
				).
				Value(&protocol),
			huh.NewInput().
				Title("Local clone path").
				Value(&localPath),
		),
		huh.NewGroup(
			huh.NewConfirm().
				Title("Clone this repo and continue?").
				Value(&confirmed),
		),
	)
	form = wireFormIO(form, deps.Accessible, deps.Input, deps.Output)

	if err := form.Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return WizardResult{}, ErrWizardCancelled
		}
		return WizardResult{}, fmt.Errorf("running setup wizard: %w", err)
	}

	if repo != "" && !githubUsernameRe.MatchString(repo) {
		return WizardResult{}, fmt.Errorf(
			"invalid GitHub username/handle %q (want alphanumeric/hyphen, no leading/trailing hyphen, max 39 characters)", repo)
	}

	localPath, err := config.ResolvePath(localPath)
	if err != nil {
		return WizardResult{}, fmt.Errorf("resolving local clone path: %w", err)
	}

	if !confirmed {
		return WizardResult{}, ErrWizardCancelled
	}

	return WizardResult{
		RepoName:      repo,
		CloneProtocol: protocol,
		LocalPath:     localPath,
	}, nil
}
