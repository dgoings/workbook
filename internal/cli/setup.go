package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/dgoings/workbook/internal/agentdocs"
	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/gitstore"
	"github.com/dgoings/workbook/internal/release"
	"github.com/dgoings/workbook/internal/userconfig"
)

type setupResult struct {
	Repository string            `json:"repository"`
	ProjectID  string            `json:"projectId"`
	Key        string            `json:"key"`
	TaskCount  int               `json:"taskCount"`
	UserConfig setupUserConfig   `json:"userConfig"`
	Config     setupConfigResult `json:"config"`
	Identity   setupIdentity     `json:"identity"`
	Docs       *agentdocs.Report `json:"docs,omitempty"`
	Sync       setupSyncResult   `json:"sync"`
}

// setupIdentity reports which record answered the question "which project is
// this". Joining an existing project and minting a new one look identical in a
// result that reports only the ID, and they are the two outcomes a user most
// needs to tell apart.
type setupIdentity struct {
	Source    string `json:"source"`
	Minted    bool   `json:"minted"`
	Published bool   `json:"published"`
}

// setupIdentityDescription renders the identity source for a human.
func setupIdentityDescription(identity setupIdentity) string {
	switch identity.Source {
	case "ref":
		return "adopted from the published project identity"
	case "file":
		if identity.Published {
			return "adopted from .workbook/config.json and published"
		}
		return "adopted from .workbook/config.json"
	case "guard":
		return "recovered from this repository's private guard and published"
	case "mint":
		return "minted and published"
	default:
		return identity.Source
	}
}

// setupConfigResult reports the project document version and whether this run
// upgraded it, because an upgrade is a tracked-file change to commit and makes
// the project unreadable to older Workbook versions.
type setupConfigResult struct {
	Version  int  `json:"version"`
	Upgraded bool `json:"upgraded"`
}

type setupUserConfig struct {
	Path    string `json:"path"`
	Created bool   `json:"created"`
}

type setupSyncResult struct {
	Status string                  `json:"status"`
	Detail string                  `json:"detail,omitempty"`
	Result *gitstore.SyncRunResult `json:"result,omitempty"`
}

// runSetup bootstraps Workbook in a clone: create or validate project
// identity, install managed agent documentation, and synchronize shared task
// refs with origin.
func runSetup(ctx context.Context, args []string, cwd string, stdout io.Writer) error {
	flags := newFlagSet("setup")
	key := flags.String("key", "WB", "project key")
	noDocs := flags.Bool("no-docs", false, "skip managed agent documentation")
	noSync := flags.Bool("no-sync", false, "skip synchronizing task refs with origin")
	skillDir := flags.String("skill-dir", "", "install the Workbook skill here")
	noSkill := flags.Bool("no-skill", false, "leave the Workbook skill alone")
	force := flags.Bool("force", false, "overwrite locally modified managed documentation")
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	if err := validateSkillFlags(*skillDir, *noSkill); err != nil {
		return err
	}

	repository, err := gitstore.Open(ctx, cwd)
	if err != nil {
		return err
	}
	// A checkout that predates the project's Workbook adoption carries no
	// tracked configuration, so Init alone would mint a second identity for a
	// project origin already has. Ask origin first; --no-sync keeps bootstrap
	// fully local.
	if !*noSync {
		if _, _, err := repository.AdoptOriginProject(ctx, *key); err != nil {
			return err
		}
	}
	config, minted, err := repository.Init(ctx, *key, core.CryptoULIDSource{})
	if err != nil {
		return err
	}
	if err := seedMintedStatuses(ctx, repository, config, minted, *noSync); err != nil {
		return err
	}

	upgraded, err := repository.UpgradeConfig(ctx)
	if err != nil {
		return err
	}
	if upgraded {
		config, err = repository.LoadConfig()
		if err != nil {
			return err
		}
	}

	user, created, err := userconfig.Ensure()
	if err != nil {
		return err
	}
	configPath, err := userconfig.Path()
	if err != nil {
		return err
	}

	result := setupResult{
		Repository: repository.Root,
		ProjectID:  config.ProjectID,
		Key:        config.Key,
		UserConfig: setupUserConfig{Path: configPath, Created: created},
		Config:     setupConfigResult{Version: config.Version, Upgraded: upgraded},
	}
	if origin, known := repository.IdentityOrigin(); known {
		result.Identity = setupIdentity{
			Source:    origin.Source,
			Minted:    origin.Minted,
			Published: origin.Published,
		}
	}

	// The vocabulary the guidelines document is the one this clone holds now,
	// which for a project joining an existing one is the built-in default until
	// the fetch below delivers the ledger. That is corrected after the sync
	// rather than before it, because documentation has to be installed even when
	// origin is unreachable.
	state, err := repository.LoadVocabularyState(ctx, config)
	if err != nil {
		return err
	}
	docsOptions := agentdocs.Options{
		Root:       repository.Root,
		Project:    config,
		Vocabulary: state.Vocabulary,
		User:       user,
		Generator:  release.Version,
		Force:      *force,
		SkillDir:   *skillDir,
		SkipSkill:  *noSkill,
	}
	if !*noDocs {
		report, err := agentdocs.Apply(docsOptions)
		if err != nil {
			return err
		}
		result.Docs = &report
	}

	// A conflict is not a bootstrap failure: the fetch completed, refs
	// advanced, and the report is what the caller needs. Setup finishes and
	// hands the conflict list back with exit 8, the same as every other command
	// whose fetch could not finish a replay.
	var syncErr error
	result.Sync, syncErr = setupSync(ctx, repository, config, *noSync)
	if syncErr != nil && core.CategoryOf(syncErr) != core.CategoryConflict {
		return syncErr
	}
	var conflicts []core.Conflict
	var configConflicts []core.ConfigConflict
	if result.Sync.Result != nil {
		conflicts = result.Sync.Result.Fetch.Conflicts
		configConflicts = result.Sync.Result.Fetch.ConfigConflicts
	}
	if result.Docs != nil {
		if err := refreshFetchedGuidelines(ctx, repository, config, docsOptions, state.Head, result.Docs); err != nil {
			return err
		}
	}

	tasks, err := repository.List(ctx, config)
	if err != nil {
		return err
	}
	result.TaskCount = len(tasks)

	if *jsonMode {
		writeSyncPhaseResultWithConfig(stdout, "setup", result, conflicts, configConflicts, nil, true, func(io.Writer) {})
		return syncErr
	}
	fmt.Fprintf(stdout, "Repository:\t%s\n", result.Repository)
	fmt.Fprintf(stdout, "Project ID:\t%s\t(%s)\n", result.ProjectID, setupIdentityDescription(result.Identity))
	fmt.Fprintf(stdout, "Key:\t%s\n", result.Key)
	suffix := ""
	if result.UserConfig.Created {
		suffix = "\t(created)"
	}
	fmt.Fprintf(stdout, "Config:\t%s%s\n", result.UserConfig.Path, suffix)
	if result.Config.Upgraded {
		fmt.Fprintf(stdout, "Project:\tupgraded to version %d\t(commit it; older Workbook versions cannot read it)\n", result.Config.Version)
	}
	if result.Docs != nil {
		for _, artifact := range result.Docs.Artifacts {
			fmt.Fprintf(stdout, "Docs:\t%s\t%s\n", artifact.Path, artifactAction(artifact))
		}
	} else {
		fmt.Fprintf(stdout, "Docs:\tskipped\n")
	}
	fmt.Fprintf(stdout, "Sync:\t%s", result.Sync.Status)
	if result.Sync.Detail != "" {
		fmt.Fprintf(stdout, "\t%s", result.Sync.Detail)
	}
	fmt.Fprintln(stdout)
	// setup is the one bootstrap command a fresh clone runs, so it is where a
	// person first meets an origin holding a ref this build cannot read. The run
	// completed despite it, and without this the report reached only a caller
	// who asked for JSON. The fetch phase is the whole report: the push phase of
	// a run repeats nothing about origin's namespace.
	if result.Sync.Result != nil {
		writeIgnoredRefs(stdout, result.Sync.Result.Remote, result.Sync.Result.Fetch.Ignored)
		// Bootstrap is where a remote that will not take the identity ref has to
		// be heard: everything downstream assumes task refs on origin have an
		// identity beside them.
		if result.Sync.Result.Identity != nil {
			if detail, found := result.Sync.Result.Identity.Warning(); found {
				fmt.Fprintf(stdout, "Identity:\t%s\n", detail)
			}
		}
		// The same holds for the configuration ledger: a clone that joins a
		// project whose statuses this remote will not hand over renders columns
		// nothing explains.
		if result.Sync.Result.Config != nil {
			if detail, found := result.Sync.Result.Config.Warning(); found {
				fmt.Fprintf(stdout, "Config:\t%s\n", detail)
			}
		}
	}
	fmt.Fprintf(stdout, "Tasks:\t%d\n", result.TaskCount)
	writeConflicts(stdout, conflicts)
	writeConfigConflicts(stdout, configConflicts)
	return syncErr
}

// seedMintedStatuses records this build's default statuses for a project that
// has no statuses recorded anywhere and no work that any statuses could be
// about.
//
// `blocked` left the default set once dependencies said what a task is waiting
// on, so a project minted today has five statuses — but a project that already
// had tasks was using six, and it keeps using them until a person runs `workbook
// status delete blocked --into backlog`. Recording the choice in the ledger
// rather than leaving it to a fallback is what makes it survive the next
// release: a genesis names the statuses, so no later build has to guess which
// era this project started in.
//
// The gate is emptiness rather than "this run minted the identity", and that
// distinction is the repair. A genesis write that failed after Init minted, or a
// ledger ref lost afterwards, leaves a project this build created reverted to
// the pre-ledger six with nothing able to put it back — while a task-less,
// unconfigured project is safe to seed no matter who created it or when, because
// there is no board to re-columnize and no recorded decision to overrule.
//
// Task refs are checked on both sides. A repository holding tasks is an existing
// project whatever its identity records say, and so is one whose tasks live only
// on origin — a fresh clone that has not fetched yet looks empty locally, and
// seeding five statuses under it would both drop a column its teammates draw and
// start a second configuration root. Origin's ledger is checked for the same
// reason: two roots is a situation the project can be spared entirely by not
// creating the second one.
//
// `--no-sync` cannot ask origin, so it falls back to the narrower local
// evidence: seed only what this run itself minted. That is the same bargain
// --no-sync makes everywhere else — bootstrap fully locally, and accept that
// what origin holds is unknown until something synchronizes.
func seedMintedStatuses(
	ctx context.Context,
	repository *gitstore.Repository,
	config core.ProjectConfig,
	minted bool,
	noSync bool,
) error {
	state, err := repository.LoadVocabularyState(ctx, config)
	if err != nil {
		return err
	}
	if state.Seeded {
		return nil
	}
	heads, err := repository.ListTaskHeads(ctx, config)
	if err != nil {
		return err
	}
	if len(heads) > 0 {
		return nil
	}
	if noSync {
		if !minted {
			return nil
		}
	} else {
		empty, err := repository.OriginHasNoWorkbookHistory(ctx)
		if err != nil {
			// An origin this run cannot reach is reported by the synchronization
			// stage a moment later. It is not this decision's to fail on, and it
			// is not evidence of an empty project either, so the project stays on
			// the fallback and a later setup seeds it once origin can be read.
			return nil
		}
		if !empty {
			return nil
		}
	}
	_, err = repository.MintConfigLedger(ctx, config, core.CryptoULIDSource{})
	return err
}

// refreshFetchedGuidelines rewrites the guidelines when the fetch delivered a
// configuration ledger the installed rendering did not know about.
//
// A clone joining a project that renamed its columns is the case this exists
// for: setup installs documentation before it fetches, so without this the very
// first thing a fresh clone reads about its statuses would be the built-in six.
// It replaces the guidelines entry in the report rather than appending one, so
// the run still reports one line per managed file.
//
// A refusal here is reported like any other, because it cannot be the ordinary
// one. Apply ran moments ago in the same command and setup returned its error
// if it was blocked, so the file this reads is one Workbook has just written:
// stale or current, never modified. Anything else means the file changed under
// the command, which is worth hearing about rather than swallowing.
func refreshFetchedGuidelines(
	ctx context.Context,
	repository *gitstore.Repository,
	config core.ProjectConfig,
	options agentdocs.Options,
	installedHead string,
	report *agentdocs.Report,
) error {
	state, err := repository.LoadVocabularyState(ctx, config)
	if err != nil {
		return err
	}
	if state.Head == installedHead {
		return nil
	}
	options.Vocabulary = state.Vocabulary
	refreshed, err := agentdocs.ApplyGuidelines(options)
	if err != nil {
		return err
	}
	for _, artifact := range refreshed.Artifacts {
		for index, existing := range report.Artifacts {
			if existing.Path == artifact.Path {
				report.Artifacts[index] = artifact
			}
		}
	}
	return nil
}

// setupSync synchronizes shared task refs unless it was skipped or the
// repository has no origin. A clone without a remote is the solo local
// workflow, not an error.
func setupSync(
	ctx context.Context,
	repository *gitstore.Repository,
	config core.ProjectConfig,
	skip bool,
) (setupSyncResult, error) {
	if skip {
		return setupSyncResult{Status: "skipped", Detail: "--no-sync"}, nil
	}
	if _, err := repository.Git(ctx, nil, "remote", "get-url", "origin"); err != nil {
		return setupSyncResult{Status: "skipped", Detail: "no origin remote configured"}, nil
	}
	run, err := repository.Sync(ctx, config)
	if err != nil {
		status := "failed"
		if core.CategoryOf(err) == core.CategoryConflict {
			status = "conflicted"
		}
		return setupSyncResult{Status: status, Detail: err.Error(), Result: &run}, err
	}
	return setupSyncResult{Status: "completed", Result: &run}, nil
}

// runDocs manages the agent documentation Workbook generates for a project.
func runDocs(ctx context.Context, args []string, cwd string, stdout, stderr io.Writer) error {
	subcommand, args, err := requiredFirstArgument("docs", "docs command", args)
	if err != nil {
		return err
	}
	switch subcommand {
	case "install", "update", "status", "remove":
	default:
		return core.Errorf(core.CategoryInvocation, "unknown docs command %q", subcommand)
	}

	flags := newFlagSet("docs", subcommand)
	var create stringListValue
	if subcommand == "install" {
		flags.Var(&create, "create", "also create this documentation target")
	}
	skillDir := flags.String("skill-dir", "", "install the Workbook skill here")
	noSkill := flags.Bool("no-skill", false, "leave the Workbook skill alone")
	var force *bool
	if subcommand != "status" {
		force = flags.Bool("force", false, "overwrite locally modified files")
	}
	jsonMode := flags.Bool("json", false, "emit JSON")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	if err := validateSkillFlags(*skillDir, *noSkill); err != nil {
		return err
	}

	repository, config, err := openRepository(ctx, cwd, stderr)
	if err != nil {
		return err
	}
	user, err := userconfig.Load()
	if err != nil {
		return err
	}

	// Read locally, like every other documentation operation: `workbook docs`
	// renders what this clone knows and never fetches to find out.
	state, err := repository.LoadVocabularyState(ctx, config)
	if err != nil {
		return err
	}

	options := agentdocs.Options{
		Root:       repository.Root,
		Project:    config,
		Vocabulary: state.Vocabulary,
		User:       user,
		Generator:  release.Version,
		Create:     create.values,
		SkillDir:   *skillDir,
		SkipSkill:  *noSkill,
	}
	if force != nil {
		options.Force = *force
	}

	var report agentdocs.Report
	switch subcommand {
	case "status":
		report, err = agentdocs.Status(options)
	case "remove":
		report, err = agentdocs.Remove(options)
	default:
		report, err = agentdocs.Apply(options)
	}
	if err != nil {
		return err
	}

	command := "docs " + subcommand
	if *jsonMode {
		writeResult(stdout, command, report)
		return nil
	}
	for _, artifact := range report.Artifacts {
		if subcommand == "status" {
			fmt.Fprintf(stdout, "%s\t%s\n", artifact.Path, artifact.State)
			continue
		}
		fmt.Fprintf(stdout, "%s\t%s\t%s\n", artifact.Path, artifact.State, artifactAction(artifact))
	}
	return nil
}

// validateSkillFlags rejects the contradictory combination rather than
// silently letting one flag win.
func validateSkillFlags(skillDir string, noSkill bool) error {
	if skillDir != "" && noSkill {
		return core.Errorf(core.CategoryInvocation, "cannot use --skill-dir with --no-skill")
	}
	return nil
}

// artifactAction says what an operation did to one managed file.
//
// An untouched absent file is its own answer rather than "unchanged": a project
// with no guidelines block reading "unchanged" beside the path would say the
// file is already current, which is the opposite of what it means. The JSON
// report has carried the distinction all along in `state`.
func artifactAction(artifact agentdocs.Artifact) string {
	switch {
	case artifact.Written:
		return "written"
	case artifact.State == agentdocs.StateAbsent:
		return "not installed"
	default:
		return "unchanged"
	}
}
