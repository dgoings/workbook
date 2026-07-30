package agentdocs

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/userconfig"
)

// Options describes a documentation operation against one project.
type Options struct {
	// Root is the project's working-tree root.
	Root string
	// Project supplies the identity and canonical values documented.
	Project core.ProjectConfig
	// User supplies the documentation targets and skill destination.
	User userconfig.Config
	// Generator is the Workbook version recorded in each stamp.
	Generator string
	// Force permits overwriting or removing locally modified artifacts.
	Force bool
	// Create names documentation targets to create even when absent. Each
	// entry must appear in the user configuration's DocTargets.
	Create []string
	// SkillDir overrides the user configuration's skill destination for this
	// project. A relative value resolves against Root. Empty means use the
	// configured value.
	SkillDir string
	// SkipSkill leaves the Workbook skill alone, for projects that package
	// skills themselves. Guidelines and documentation targets are unaffected.
	SkipSkill bool
}

// Artifact reports one managed file.
type Artifact struct {
	// Path is relative to the project root, or absolute when the skill is
	// installed outside the project.
	Path string `json:"path"`
	// State describes how the file compared before the operation.
	State State `json:"state"`
	// Written reports whether the operation changed the file.
	Written bool `json:"written"`
}

// Report lists every managed artifact an operation considered.
type Report struct {
	Artifacts []Artifact `json:"artifacts"`
}

// Blocked reports the artifacts that could not be changed because they were
// modified locally and Force was not set.
func (r Report) Blocked() []Artifact {
	var blocked []Artifact
	for _, artifact := range r.Artifacts {
		if artifact.State == StateModified && !artifact.Written {
			blocked = append(blocked, artifact)
		}
	}
	return blocked
}

// Stale reports the artifacts that do not match their expected content.
func (r Report) Stale() []Artifact {
	var stale []Artifact
	for _, artifact := range r.Artifacts {
		if artifact.State != StateCurrent {
			stale = append(stale, artifact)
		}
	}
	return stale
}

type target struct {
	// path is the absolute location on disk.
	path string
	// display is what the report shows.
	display  string
	document Document
	// owned marks a file Workbook generates in full, which is deleted rather
	// than emptied when the managed block is removed.
	owned bool
}

// Status reports the state of every managed artifact without writing.
func Status(options Options) (Report, error) {
	targets, err := plan(options)
	if err != nil {
		return Report{}, err
	}

	report := Report{}
	for _, item := range targets {
		existing, err := read(item.path)
		if err != nil {
			return Report{}, err
		}
		outcome := item.document.Reconcile(existing)
		report.Artifacts = append(report.Artifacts, Artifact{Path: item.display, State: outcome.State})
	}
	return report, nil
}

// Apply installs or refreshes every managed artifact. Artifacts that were
// modified locally are left alone unless Force is set; the remaining artifacts
// are still applied, and the returned error names what was skipped.
func Apply(options Options) (Report, error) {
	targets, err := plan(options)
	if err != nil {
		return Report{}, err
	}

	report := Report{}
	for _, item := range targets {
		existing, err := read(item.path)
		if err != nil {
			return report, err
		}
		outcome := item.document.Reconcile(existing)
		artifact := Artifact{Path: item.display, State: outcome.State}

		if outcome.Changed && (outcome.State != StateModified || options.Force) {
			if err := write(item.path, outcome.Contents); err != nil {
				return report, err
			}
			artifact.Written = true
		}
		report.Artifacts = append(report.Artifacts, artifact)
	}
	return report, blockedError(report, "refresh")
}

// Remove strips managed content and deletes generated files. Artifacts that
// were modified locally are preserved unless Force is set.
func Remove(options Options) (Report, error) {
	targets, err := plan(options)
	if err != nil {
		return Report{}, err
	}

	report := Report{}
	for _, item := range targets {
		existing, err := read(item.path)
		if err != nil {
			return report, err
		}
		if len(existing) == 0 {
			report.Artifacts = append(report.Artifacts, Artifact{Path: item.display, State: StateAbsent})
			continue
		}

		state, stripped := Strip(existing)
		artifact := Artifact{Path: item.display, State: state}
		switch {
		case state == StateAbsent:
		case state == StateModified && !options.Force:
		case item.owned:
			// Workbook owns the whole file, so removing the block removes it.
			if err := os.Remove(item.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return report, core.Wrap(core.CategoryOperational, "remove "+item.display, err)
			}
			artifact.Written = true
		default:
			if err := write(item.path, stripped); err != nil {
				return report, err
			}
			artifact.Written = true
		}
		report.Artifacts = append(report.Artifacts, artifact)
	}
	return report, blockedError(report, "remove")
}

func plan(options Options) ([]target, error) {
	guidelines := Document{
		Generator: options.Generator,
		Body:      RenderGuidelines(options.Project),
	}

	targets := []target{{
		path:     filepath.Join(options.Root, filepath.FromSlash(GuidelinesPath)),
		display:  GuidelinesPath,
		document: guidelines,
		owned:    true,
	}}

	if !options.SkipSkill {
		skill, err := skillDocument(options.Generator)
		if err != nil {
			return nil, err
		}
		skillDirectory := options.SkillDir
		if skillDirectory == "" {
			skillDirectory = options.User.SkillDir
		}
		if skillDirectory == "" {
			skillDirectory = userconfig.Default().SkillDir
		}
		skillPath := filepath.Join(skillDirectory, "workbook", "SKILL.md")
		display := filepath.ToSlash(skillPath)
		if !filepath.IsAbs(skillDirectory) {
			skillPath = filepath.Join(options.Root, skillPath)
		} else {
			display = skillPath
		}
		targets = append(targets, target{path: skillPath, display: display, document: skill, owned: true})
	}

	reference := Document{Generator: options.Generator, Body: RenderReference()}
	for _, name := range options.Create {
		if !slices.Contains(options.User.DocTargets, name) {
			return nil, core.Errorf(core.CategoryInvocation,
				"cannot create %q: it is not a configured documentation target", name)
		}
	}
	for _, name := range options.User.DocTargets {
		path := filepath.Join(options.Root, filepath.FromSlash(name))
		if !slices.Contains(options.Create, name) {
			if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
				continue
			} else if err != nil {
				return nil, core.Wrap(core.CategoryOperational, "inspect "+name, err)
			}
		}
		targets = append(targets, target{path: path, display: name, document: reference})
	}
	return targets, nil
}

func blockedError(report Report, action string) error {
	blocked := report.Blocked()
	if len(blocked) == 0 {
		return nil
	}
	names := make([]string, 0, len(blocked))
	for _, artifact := range blocked {
		names = append(names, artifact.Path)
	}
	return core.Errorf(core.CategoryValidation,
		"cannot %s locally modified %s; rerun with --force to overwrite",
		action, joinNames(names))
}

func joinNames(names []string) string {
	switch len(names) {
	case 1:
		return names[0]
	case 2:
		return names[0] + " and " + names[1]
	default:
		joined := ""
		for index, name := range names[:len(names)-1] {
			if index > 0 {
				joined += ", "
			}
			joined += name
		}
		return joined + ", and " + names[len(names)-1]
	}
}

func read(path string) ([]byte, error) {
	contents, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, core.Wrap(core.CategoryOperational, "read "+path, err)
	}
	return contents, nil
}

func write(path string, contents []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return core.Wrap(core.CategoryOperational, "create directory for "+path, err)
	}
	if err := os.WriteFile(path, contents, 0o644); err != nil {
		return core.Wrap(core.CategoryOperational, "write "+path, err)
	}
	return nil
}
