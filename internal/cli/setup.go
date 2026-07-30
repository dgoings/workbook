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
	Docs       *agentdocs.Report `json:"docs,omitempty"`
	Sync       setupSyncResult   `json:"sync"`
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
	config, _, err := repository.Init(ctx, *key, core.CryptoULIDSource{})
	if err != nil {
		return err
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
	}

	if !*noDocs {
		report, err := agentdocs.Apply(agentdocs.Options{
			Root:      repository.Root,
			Project:   config,
			User:      user,
			Generator: release.Version,
			Force:     *force,
			SkillDir:  *skillDir,
			SkipSkill: *noSkill,
		})
		if err != nil {
			return err
		}
		result.Docs = &report
	}

	result.Sync, err = setupSync(ctx, repository, config, *noSync)
	if err != nil {
		return err
	}

	tasks, err := repository.List(ctx, config)
	if err != nil {
		return err
	}
	result.TaskCount = len(tasks)

	if *jsonMode {
		writeResult(stdout, "setup", result)
		return nil
	}
	fmt.Fprintf(stdout, "Repository:\t%s\n", result.Repository)
	fmt.Fprintf(stdout, "Project ID:\t%s\n", result.ProjectID)
	fmt.Fprintf(stdout, "Key:\t%s\n", result.Key)
	suffix := ""
	if result.UserConfig.Created {
		suffix = "\t(created)"
	}
	fmt.Fprintf(stdout, "Config:\t%s%s\n", result.UserConfig.Path, suffix)
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
	fmt.Fprintf(stdout, "Tasks:\t%d\n", result.TaskCount)
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
		return setupSyncResult{Status: "failed", Result: &run}, err
	}
	return setupSyncResult{Status: "completed", Result: &run}, nil
}

// runDocs manages the agent documentation Workbook generates for a project.
func runDocs(ctx context.Context, args []string, cwd string, stdout io.Writer) error {
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

	repository, config, err := openRepository(ctx, cwd)
	if err != nil {
		return err
	}
	user, err := userconfig.Load()
	if err != nil {
		return err
	}

	options := agentdocs.Options{
		Root:      repository.Root,
		Project:   config,
		User:      user,
		Generator: release.Version,
		Create:    create.values,
		SkillDir:  *skillDir,
		SkipSkill: *noSkill,
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

func artifactAction(artifact agentdocs.Artifact) string {
	if artifact.Written {
		return "written"
	}
	return "unchanged"
}
