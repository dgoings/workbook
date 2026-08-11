package gitstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/dgoings/workbook/internal/core"
)

const (
	configPath    = ".workbook/config.json"
	projectFormat = "workbook.project"
	// projectVersion is the version Workbook writes. Version 1 documents
	// predate the automatic synchronization policy and remain readable.
	projectVersion       = 2
	legacyProjectVersion = 1
	projectGuard         = "project.json"
)

// Init establishes a repository's project identity and its tracked Workbook
// configuration, reporting whether this call minted a new project.
//
// Identity resolution is the shared precedence chain — the identity ref, then
// the tracked configuration, then the private guard — extended with the one
// thing only Init may do: mint. Whatever answers, the tracked configuration is
// written only when it is absent. An existing document's identity fields are
// never rewritten, because a v0.4.x teammate reads them and a bootstrap must
// not change what they see.
func (r *Repository) Init(ctx context.Context, key string, ids core.IDSource) (core.ProjectConfig, bool, error) {
	if err := r.verifyIdentity(ctx); err != nil {
		return core.ProjectConfig{}, false, err
	}
	if err := core.ValidateProjectKey(key); err != nil {
		return core.ProjectConfig{}, false, err
	}
	if err := r.ensurePrivateCache(); err != nil {
		return core.ProjectConfig{}, false, err
	}

	resolution, err := r.resolveIdentity(ctx, func() (core.ProjectIdentity, error) {
		return mintProjectIdentity(key, ids)
	})
	if err != nil {
		return core.ProjectConfig{}, false, err
	}
	config := configFromIdentity(resolution.Identity)
	if err := validateRequestedProjectKey(key, config); err != nil {
		return core.ProjectConfig{}, false, err
	}

	tracked, trackedExists, err := r.readConfig()
	if err != nil {
		return core.ProjectConfig{}, false, err
	}
	if !trackedExists {
		if err := r.writeConfig(config); err != nil {
			return core.ProjectConfig{}, false, err
		}
	} else {
		config.Version = tracked.Version
		config.AutoSync = tracked.AutoSync
	}
	r.rememberIdentity(resolution)
	return r.rememberConfig(config), resolution.Minted, nil
}

func mintProjectIdentity(key string, ids core.IDSource) (core.ProjectIdentity, error) {
	if ids == nil {
		return core.ProjectIdentity{}, core.Errorf(core.CategoryOperational, "project ID source is required")
	}
	projectID, err := ids.New()
	if err != nil {
		return core.ProjectIdentity{}, core.Wrap(core.CategoryOperational, "cannot generate project ID", err)
	}
	if err := validateProjectID(projectID); err != nil {
		return core.ProjectIdentity{}, err
	}
	return core.ProjectIdentity{
		Format:    core.ProjectIdentityFormat,
		Version:   core.ProjectIdentityVersion,
		ProjectID: projectID,
		Key:       key,
	}, nil
}

// AdoptOriginProject joins the Workbook project that origin already carries.
//
// A checkout that predates a project's Workbook adoption has no tracked
// configuration for Init to find, and minting a fresh identity there splits
// the repository into two projects. So before Init may mint, bootstrap asks
// origin, in two escalating steps.
//
// The first is origin's identity ref. It is authoritative, needs no branch
// checkout, and needs nothing committed anywhere, so a clone sitting on a
// pre-Workbook branch joins the real project instead of inventing one. The
// second is the older probe kept for projects that have not published the ref
// yet: a configuration committed on origin's default branch is adopted into
// the working tree, and task refs on origin without any such configuration
// stop bootstrap, because the tasks prove a project exists that this probe
// cannot name.
//
// The probe runs only when the identity ref, the tracked configuration, and
// the guard are all absent locally; callers skip it entirely when the user
// asked for --no-sync.
func (r *Repository) AdoptOriginProject(ctx context.Context, key string) (core.ProjectConfig, bool, error) {
	if err := r.verifyIdentity(ctx); err != nil {
		return core.ProjectConfig{}, false, err
	}
	if err := core.ValidateProjectKey(key); err != nil {
		return core.ProjectConfig{}, false, err
	}
	if _, exists, err := r.readIdentityRef(ctx, identityRef); err != nil || exists {
		return core.ProjectConfig{}, false, err
	}
	if _, exists, err := r.readConfig(); err != nil || exists {
		return core.ProjectConfig{}, false, err
	}
	if _, exists, err := r.readProjectGuard(); err != nil || exists {
		return core.ProjectConfig{}, false, err
	}
	if _, err := r.Git(ctx, nil, "remote", "get-url", "origin"); err != nil {
		return core.ProjectConfig{}, false, nil
	}

	listing, err := r.Git(ctx, nil, "ls-remote", "origin", "HEAD", identityRef, taskRefPrefix+"*")
	if err != nil {
		return core.ProjectConfig{}, false, core.Wrap(core.CategoryOperational,
			"cannot ask origin whether a Workbook project already exists; use --no-sync to bootstrap without consulting origin", err)
	}
	head, identity, tasks := parseOriginProbe(listing)
	if !head && !identity && !tasks {
		return core.ProjectConfig{}, false, nil
	}
	if identity {
		adopted, err := r.adoptOriginIdentityRef(ctx, key)
		if err != nil {
			return core.ProjectConfig{}, false, err
		}
		return adopted, true, nil
	}

	var discovered core.ProjectConfig
	found := false
	if head {
		discovered, found, err = r.readOriginHeadConfig(ctx)
		if err != nil {
			return core.ProjectConfig{}, false, err
		}
	}
	if !found {
		if tasks {
			return core.ProjectConfig{}, false, core.Errorf(core.CategoryOperational,
				"origin already has Workbook task refs, but neither %s nor its default branch's .workbook/config.json names their project; check out or merge the branch that adds .workbook/config.json, then rerun workbook setup",
				identityRef)
		}
		return core.ProjectConfig{}, false, nil
	}
	if discovered.Key != key {
		return core.ProjectConfig{}, false, core.Errorf(core.CategoryValidation,
			"origin already has a Workbook project with key %q; rerun workbook setup --key %q to join it", discovered.Key, discovered.Key)
	}
	if err := r.writeConfig(discovered); err != nil {
		return core.ProjectConfig{}, false, err
	}
	return discovered, true, nil
}

// adoptOriginIdentityRef fetches origin's canonical identity and makes it this
// repository's own, then materializes the advisory tracked configuration so a
// v0.4.x teammate reading this branch still finds the project.
func (r *Repository) adoptOriginIdentityRef(ctx context.Context, key string) (core.ProjectConfig, error) {
	if _, err := r.Git(ctx, nil,
		"fetch", "--no-tags", "--no-auto-maintenance", "origin", identityFetchRefspec); err != nil {
		return core.ProjectConfig{}, core.Wrap(core.CategoryOperational,
			"cannot fetch origin's Workbook project identity", err)
	}
	record, found, err := r.readIdentityRef(ctx, remoteIdentityRef)
	if err != nil {
		return core.ProjectConfig{}, err
	}
	if !found {
		return core.ProjectConfig{}, core.Errorf(core.CategoryOperational,
			"origin listed %s but did not deliver it; rerun workbook setup", identityRef)
	}
	if record.Identity.Key != key {
		return core.ProjectConfig{}, core.Errorf(core.CategoryValidation,
			"origin already has a Workbook project with key %q; rerun workbook setup --key %q to join it",
			record.Identity.Key, record.Identity.Key)
	}

	if err := r.createRef(ctx, identityRef, record.Head); err != nil {
		local, exists, readErr := r.readIdentityRef(ctx, identityRef)
		if readErr != nil {
			return core.ProjectConfig{}, readErr
		}
		if !exists {
			return core.ProjectConfig{}, core.Wrap(core.CategoryOperational,
				"cannot adopt origin's Workbook project identity", err)
		}
		if !local.Identity.SameIdentity(record.Identity) {
			return core.ProjectConfig{}, core.Errorf(core.CategoryCorruptData,
				"%s names project %s (key %s), but origin's identity names project %s (key %s)",
				identityRef, local.Identity.ProjectID, local.Identity.Key,
				record.Identity.ProjectID, record.Identity.Key)
		}
	}
	config := configFromIdentity(record.Identity)
	if err := r.writeConfig(config); err != nil {
		return core.ProjectConfig{}, err
	}
	return config, nil
}

// parseOriginProbe reads one ls-remote listing of origin's HEAD, its Workbook
// identity ref, and its Workbook task namespace.
func parseOriginProbe(listing []byte) (head, identity, tasks bool) {
	for _, line := range strings.Split(string(listing), "\n") {
		_, refname, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		if refname == "HEAD" {
			head = true
		}
		if refname == identityRef {
			identity = true
		}
		if strings.HasPrefix(refname, taskRefPrefix) {
			tasks = true
		}
	}
	return head, identity, tasks
}

// readOriginHeadConfig fetches origin's default branch and decodes the
// tracked Workbook configuration it carries, if any. It reports absence
// rather than failure when the branch has no configuration: a plain project
// adopting Workbook for the first time looks exactly like that.
func (r *Repository) readOriginHeadConfig(ctx context.Context) (core.ProjectConfig, bool, error) {
	if _, err := r.Git(ctx, nil, "fetch", "--no-tags", "--no-auto-maintenance", "origin", "HEAD"); err != nil {
		return core.ProjectConfig{}, false, core.Wrap(core.CategoryOperational, "cannot fetch origin's default branch", err)
	}
	oid, err := r.Git(ctx, nil, "rev-parse", "--verify", "--quiet", "FETCH_HEAD:"+configPath)
	if err != nil {
		return core.ProjectConfig{}, false, nil
	}
	blob, err := gitSingleLine(oid)
	if err != nil {
		return core.ProjectConfig{}, false, core.Errorf(core.CategoryOperational, "cannot resolve origin's Workbook configuration: %v", err)
	}
	contents, err := r.Git(ctx, nil, "cat-file", "blob", blob)
	if err != nil {
		return core.ProjectConfig{}, false, err
	}
	config, err := decodeConfig(contents)
	if err != nil {
		return core.ProjectConfig{}, false, core.Wrap(core.CategoryCorruptData,
			"origin's default branch carries a Workbook configuration this version cannot adopt", err)
	}
	return config, true, nil
}

// LoadConfig returns the repository's validated Workbook configuration.
//
// Its signature is deliberately unchanged: every command opens a repository
// through it and roughly a dozen call sites compare the result with ==. What
// changed underneath is where each field comes from. The project ID and key are
// the canonical identity, resolved through the identity ref; the document
// version and the automatic synchronization policy are mutable preferences and
// still come from the tracked file. A repository whose branch carries no
// tracked file is therefore usable when the identity ref says which project it
// is — which is exactly the checkout that used to look like a new project.
func (r *Repository) LoadConfig() (core.ProjectConfig, error) {
	r.metadataMu.RLock()
	loaded, config := r.configLoaded, r.config
	r.metadataMu.RUnlock()
	if loaded {
		return config, nil
	}

	// Identity resolution runs Git, and the metadata mutex guards fields the
	// Git helpers take for themselves, so it happens outside the lock. The
	// context is the process-wide one: LoadConfig is called from paths that
	// predate any context and keeps its signature.
	identity, err := r.LoadIdentity(context.Background())
	if err != nil {
		return core.ProjectConfig{}, err
	}
	resolved := configFromIdentity(identity)

	tracked, exists, err := r.readConfig()
	if err != nil {
		return core.ProjectConfig{}, err
	}
	if exists {
		if !tracked.SameIdentity(resolved) {
			guard, guardExists, guardErr := r.readProjectGuard()
			if guardErr != nil {
				return core.ProjectConfig{}, guardErr
			}
			return core.ProjectConfig{}, r.identityMismatch(identity, tracked, guardExists, guard)
		}
		resolved.Version = tracked.Version
		resolved.AutoSync = tracked.AutoSync
	}
	return r.rememberConfig(resolved), nil
}

// UpgradeConfig rewrites a legacy tracked configuration at the version
// Workbook currently writes, reporting whether it changed anything.
//
// Only setup calls this. Ordinary reads and writes accept a legacy document as
// it stands, because upgrading is a tracked-file change the user has to commit
// and it makes the project unreadable to older Workbook binaries. That belongs
// to a command the user ran deliberately, not to every mutation.
func (r *Repository) UpgradeConfig(ctx context.Context) (bool, error) {
	if err := r.verifyIdentity(ctx); err != nil {
		return false, err
	}
	tracked, exists, err := r.readConfig()
	if err != nil {
		return false, err
	}
	if !exists {
		return false, core.Errorf(core.CategoryNotInitialized, "Workbook is not initialized")
	}
	if tracked.Version == projectVersion {
		return false, nil
	}

	upgraded := tracked
	upgraded.Version = projectVersion
	if err := r.writeConfig(upgraded); err != nil {
		return false, err
	}
	r.replaceConfig(upgraded)
	return true, nil
}

// SetProjectAutoSync records the project's automatic synchronization policy,
// writing the configuration at the current document version. Passing
// core.AutoSyncUnset clears the policy so the user configuration decides again.
func (r *Repository) SetProjectAutoSync(ctx context.Context, setting core.AutoSyncSetting) (core.ProjectConfig, error) {
	if err := r.verifyIdentity(ctx); err != nil {
		return core.ProjectConfig{}, err
	}
	tracked, exists, err := r.readConfig()
	if err != nil {
		return core.ProjectConfig{}, err
	}
	if !exists {
		return core.ProjectConfig{}, core.Errorf(core.CategoryNotInitialized, "Workbook is not initialized")
	}

	updated := tracked
	updated.Version = projectVersion
	updated.AutoSync = setting
	if err := r.writeConfig(updated); err != nil {
		return core.ProjectConfig{}, err
	}
	r.replaceConfig(updated)
	return updated, nil
}

// replaceConfig updates the memoized configuration after this process rewrote
// it, so later work in the same command does not use the superseded document.
func (r *Repository) replaceConfig(config core.ProjectConfig) {
	r.metadataMu.Lock()
	defer r.metadataMu.Unlock()
	r.config = config
	r.configLoaded = true
}

func (r *Repository) rememberConfig(config core.ProjectConfig) core.ProjectConfig {
	r.metadataMu.Lock()
	defer r.metadataMu.Unlock()
	if !r.configLoaded {
		r.config = config
		r.configLoaded = true
	}
	return r.config
}

// guardMismatch reports a tracked configuration and common project guard that
// name different projects. The guard lives inside the common Git directory,
// out of reach of any working-tree cleanup, so the error itself must name the
// file and the way out.
//
// From v0.5.0 this is reachable only while no identity ref exists. Once one
// does, it arbitrates and a disagreeing guard is repaired from it instead.
func (r *Repository) guardMismatch(tracked, guard core.ProjectConfig) error {
	return core.Errorf(core.CategoryCorruptData,
		"tracked Workbook configuration does not match common project guard: %s names project %s (key %s) but %s names project %s (key %s); if the tracked configuration is this repository's project, delete the guard file and rerun the command — Workbook republishes the guard from the tracked configuration",
		filepath.Join(r.Root, configPath), tracked.ProjectID, tracked.Key,
		filepath.Join(r.CommonGitDir, "workbook", projectGuard), guard.ProjectID, guard.Key)
}

func (r *Repository) readConfig() (core.ProjectConfig, bool, error) {
	return readConfigFile(filepath.Join(r.Root, configPath), "Workbook configuration")
}

func (r *Repository) readProjectGuard() (core.ProjectConfig, bool, error) {
	return readConfigFile(filepath.Join(r.CommonGitDir, "workbook", projectGuard), "Workbook common project guard")
}

func readConfigFile(path, description string) (core.ProjectConfig, bool, error) {
	contents, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return core.ProjectConfig{}, false, nil
	}
	if err != nil {
		return core.ProjectConfig{}, false, core.Wrap(core.CategoryOperational, "cannot read "+description, err)
	}
	config, err := decodeConfig(contents)
	if err != nil {
		return core.ProjectConfig{}, false, err
	}
	return config, true, nil
}

func (r *Repository) publishProjectGuard(candidate core.ProjectConfig) (core.ProjectConfig, bool, error) {
	contents, err := encodeConfig(candidate)
	if err != nil {
		return core.ProjectConfig{}, false, err
	}
	cacheDir := filepath.Join(r.CommonGitDir, "workbook")
	temporary, err := os.CreateTemp(cacheDir, ".project-*.tmp")
	if err != nil {
		return core.ProjectConfig{}, false, core.Wrap(core.CategoryOperational, "cannot create temporary Workbook project guard", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)

	if _, err := temporary.Write(contents); err != nil {
		temporary.Close()
		return core.ProjectConfig{}, false, core.Wrap(core.CategoryOperational, "cannot write Workbook project guard", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return core.ProjectConfig{}, false, core.Wrap(core.CategoryOperational, "cannot sync Workbook project guard", err)
	}
	if err := temporary.Close(); err != nil {
		return core.ProjectConfig{}, false, core.Wrap(core.CategoryOperational, "cannot close Workbook project guard", err)
	}
	if err := os.Chmod(temporaryPath, 0o644); err != nil {
		return core.ProjectConfig{}, false, core.Wrap(core.CategoryOperational, "cannot set Workbook project guard permissions", err)
	}

	guardPath := filepath.Join(cacheDir, projectGuard)
	if err := os.Link(temporaryPath, guardPath); err == nil {
		if err := syncDirectory(cacheDir); err != nil {
			return core.ProjectConfig{}, false, err
		}
		return candidate, true, nil
	} else if !errors.Is(err, os.ErrExist) {
		return core.ProjectConfig{}, false, core.Wrap(core.CategoryOperational, "cannot publish Workbook project guard", err)
	}

	persisted, exists, err := r.readProjectGuard()
	if err != nil {
		return core.ProjectConfig{}, false, err
	}
	if !exists {
		return core.ProjectConfig{}, false, core.Errorf(core.CategoryOperational, "Workbook project guard disappeared during initialization")
	}
	return persisted, false, nil
}

// repairProjectGuard overwrites the private guard with the canonical identity.
//
// Publication uses a link, which is exactly-once and therefore cannot replace
// anything; repair is a deliberate replacement of a record the identity ref has
// already overruled, so it renames over the existing file instead.
func (r *Repository) repairProjectGuard(config core.ProjectConfig) error {
	contents, err := encodeConfig(config)
	if err != nil {
		return err
	}
	cacheDir := filepath.Join(r.CommonGitDir, "workbook")
	temporary, err := os.CreateTemp(cacheDir, ".project-*.tmp")
	if err != nil {
		return core.Wrap(core.CategoryOperational, "cannot create temporary Workbook project guard", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)

	if _, err := temporary.Write(contents); err != nil {
		temporary.Close()
		return core.Wrap(core.CategoryOperational, "cannot write Workbook project guard", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return core.Wrap(core.CategoryOperational, "cannot sync Workbook project guard", err)
	}
	if err := temporary.Close(); err != nil {
		return core.Wrap(core.CategoryOperational, "cannot close Workbook project guard", err)
	}
	if err := os.Chmod(temporaryPath, 0o644); err != nil {
		return core.Wrap(core.CategoryOperational, "cannot set Workbook project guard permissions", err)
	}
	if err := os.Rename(temporaryPath, filepath.Join(cacheDir, projectGuard)); err != nil {
		return core.Wrap(core.CategoryOperational, "cannot repair Workbook project guard", err)
	}
	return syncDirectory(cacheDir)
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return core.Wrap(core.CategoryOperational, "cannot open Workbook private cache for sync", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return core.Wrap(core.CategoryOperational, "cannot sync Workbook private cache", err)
	}
	return nil
}

func validateRequestedProjectKey(requested string, config core.ProjectConfig) error {
	if config.Key != requested {
		return core.Errorf(core.CategoryValidation, "repository is already initialized with project key %q", config.Key)
	}
	return nil
}

func (r *Repository) writeConfig(config core.ProjectConfig) error {
	contents, err := encodeConfig(config)
	if err != nil {
		return err
	}
	configDir := filepath.Join(r.Root, ".workbook")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return core.Wrap(core.CategoryOperational, "cannot create Workbook configuration directory", err)
	}

	temporary, err := os.CreateTemp(configDir, ".config-*.tmp")
	if err != nil {
		return core.Wrap(core.CategoryOperational, "cannot create temporary Workbook configuration", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)

	if _, err := temporary.Write(contents); err != nil {
		temporary.Close()
		return core.Wrap(core.CategoryOperational, "cannot write Workbook configuration", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return core.Wrap(core.CategoryOperational, "cannot sync Workbook configuration", err)
	}
	if err := temporary.Close(); err != nil {
		return core.Wrap(core.CategoryOperational, "cannot close Workbook configuration", err)
	}
	if err := os.Chmod(temporaryPath, 0o644); err != nil {
		return core.Wrap(core.CategoryOperational, "cannot set Workbook configuration permissions", err)
	}
	if err := os.Rename(temporaryPath, filepath.Join(r.Root, configPath)); err != nil {
		return core.Wrap(core.CategoryOperational, "cannot install Workbook configuration", err)
	}
	return nil
}

func (r *Repository) ensurePrivateCache() error {
	cacheDir := filepath.Join(r.CommonGitDir, "workbook")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return core.Wrap(core.CategoryOperational, "cannot create Workbook private cache", err)
	}
	if err := os.Chmod(cacheDir, 0o755); err != nil {
		return core.Wrap(core.CategoryOperational, "cannot set Workbook private cache permissions", err)
	}
	return nil
}

func encodeConfig(config core.ProjectConfig) ([]byte, error) {
	contents, err := json.Marshal(config)
	if err != nil {
		return nil, core.Wrap(core.CategoryValidation, "cannot encode Workbook configuration", err)
	}
	return append(contents, '\n'), nil
}

func decodeConfig(contents []byte) (core.ProjectConfig, error) {
	var config core.ProjectConfig
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return core.ProjectConfig{}, core.Wrap(core.CategoryCorruptData, "cannot decode Workbook configuration", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return core.ProjectConfig{}, core.Errorf(core.CategoryCorruptData, "Workbook configuration contains more than one JSON value")
		}
		return core.ProjectConfig{}, core.Wrap(core.CategoryCorruptData, "cannot decode Workbook configuration suffix", err)
	}
	if config.Format != projectFormat {
		return core.ProjectConfig{}, core.Errorf(core.CategoryCorruptData, "unsupported Workbook configuration format %q", config.Format)
	}
	if !supportedProjectVersion(config.Version) {
		return core.ProjectConfig{}, core.Errorf(core.CategoryCorruptData, "unsupported Workbook configuration version %d", config.Version)
	}
	if config.Version == legacyProjectVersion && config.AutoSync != core.AutoSyncUnset {
		return core.ProjectConfig{}, core.Errorf(core.CategoryCorruptData,
			"Workbook configuration version %d cannot carry an automatic synchronization policy", legacyProjectVersion)
	}
	if err := validateProjectID(config.ProjectID); err != nil {
		return core.ProjectConfig{}, core.Wrap(core.CategoryCorruptData, "Workbook configuration project ID is invalid", err)
	}
	if err := core.ValidateProjectKey(config.Key); err != nil {
		return core.ProjectConfig{}, core.Wrap(core.CategoryCorruptData, "Workbook configuration project key is invalid", err)
	}
	canonical, err := encodeConfig(config)
	if err != nil {
		return core.ProjectConfig{}, core.Wrap(core.CategoryCorruptData, "cannot canonicalize Workbook configuration", err)
	}
	if !bytes.Equal(contents, canonical) {
		return core.ProjectConfig{}, core.Errorf(core.CategoryCorruptData, "Workbook configuration is not canonical")
	}
	return config, nil
}

// supportedProjectVersion reports whether Workbook can operate on a project
// configuration document of this version.
//
// Every read and write path consults this, not projectVersion. Init never
// rewrites an existing configuration, so a repository initialized before the
// automatic synchronization policy keeps its version 1 document indefinitely
// and must remain fully usable.
func supportedProjectVersion(version int) bool {
	return version == projectVersion || version == legacyProjectVersion
}

func validateProjectID(projectID string) error {
	return core.ValidateProjectID(projectID)
}
