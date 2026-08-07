package perf

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dgoings/workbook/internal/testenv"
)

func smallStorageFixtureSpec(objectFormat string, operations int) FixtureSpec {
	return FixtureSpec{
		TotalTasks:        6,
		ActiveTasks:       5,
		TombstonedTasks:   1,
		OperationsPerTask: operations,
		ObjectFormat:      objectFormat,
	}
}

func buildPackedStorageFixture(t *testing.T, objectFormat string, operations int) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "fixture")
	if _, err := BuildFixture(context.Background(), root, smallStorageFixtureSpec(objectFormat, operations)); err != nil {
		t.Fatal(err)
	}
	if err := prepareStorageRepository(context.Background(), 60*time.Second, root); err != nil {
		t.Fatal(err)
	}
	return root
}

// Mutation witness: dropping any object class, classifying a blob by guessing
// from counts, or reaching an object through more than one class breaks the
// exact per-class totals and the classified/reachable equality below.
func TestMeasureGitStorageClassifiesEveryReachableObjectExactlyOnce(t *testing.T) {
	const operations = 4
	root := buildPackedStorageFixture(t, "sha1", operations)

	account, err := measureGitStorage(context.Background(), 60*time.Second, root)
	if err != nil {
		t.Fatal(err)
	}

	spec := smallStorageFixtureSpec("sha1", operations)
	commits := int64(spec.TotalTasks * spec.OperationsPerTask)
	want := map[string]int64{
		ObjectClassCommit:        commits,
		ObjectClassTree:          commits,
		ObjectClassOperationBlob: commits,
		ObjectClassStateBlob:     commits,
		ObjectClassOtherBlob:     0,
		ObjectClassAnnotatedTag:  0,
	}
	got := make(map[string]int64, len(account.Classes))
	var classTotal, rawTotal, diskTotal int64
	for _, class := range account.Classes {
		if _, duplicate := got[class.Class]; duplicate {
			t.Fatalf("class %q reported twice", class.Class)
		}
		got[class.Class] = class.Objects
		classTotal += class.Objects
		rawTotal += class.RawBytes
		diskTotal += class.DiskBytes
		if class.Objects > 0 && (class.RawBytes <= 0 || class.DiskBytes <= 0) {
			t.Fatalf("class %q has %d objects but rawBytes %d and diskBytes %d", class.Class, class.Objects, class.RawBytes, class.DiskBytes)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("class object counts = %#v, want %#v", got, want)
	}
	if account.ReachableObjects != classTotal {
		t.Fatalf("reachable objects = %d, classified across classes = %d", account.ReachableObjects, classTotal)
	}
	if account.ClassifiedObjects != account.ReachableObjects || account.UnclassifiedObjects != 0 {
		t.Fatalf("classified %d of %d reachable objects with %d unclassified", account.ClassifiedObjects, account.ReachableObjects, account.UnclassifiedObjects)
	}
	if account.ReachableRawBytes != rawTotal || account.ReachableDiskBytes != diskTotal {
		t.Fatalf("reachable totals raw %d disk %d, want class sums raw %d disk %d", account.ReachableRawBytes, account.ReachableDiskBytes, rawTotal, diskTotal)
	}

	// Independent oracle: after packing and pruning, every object Git counts
	// in the pack must be exactly the object set reachable from Workbook refs.
	if account.PackedObjects != account.ReachableObjects {
		t.Fatalf("git count-objects in-pack = %d, reachable from Workbook refs = %d", account.PackedObjects, account.ReachableObjects)
	}
	if account.LooseObjects != 0 {
		t.Fatalf("loose objects after packing = %d, want 0", account.LooseObjects)
	}
	if account.PackFileBytes <= 0 || account.PackIndexBytes <= 0 {
		t.Fatalf("pack bytes = %d and index bytes = %d, want positive sizes", account.PackFileBytes, account.PackIndexBytes)
	}
	if account.ObjectFormat != "sha1" {
		t.Fatalf("object format = %q, want sha1", account.ObjectFormat)
	}
	if account.TaskRefs != int64(spec.TotalTasks) || account.WorkbookRefs != int64(spec.TotalTasks) {
		t.Fatalf("task refs = %d and Workbook refs = %d, want %d each", account.TaskRefs, account.WorkbookRefs, spec.TotalTasks)
	}
	if account.RefPrefix != workbookRefPrefix {
		t.Fatalf("ref prefix = %q, want %q", account.RefPrefix, workbookRefPrefix)
	}
}

// Mutation witness: reporting raw content bytes where on-disk bytes belong (or
// the reverse) collapses the documented distinction; compressed JSON documents
// must be strictly smaller on disk than raw.
func TestMeasureGitStorageSeparatesRawContentBytesFromOnDiskBytes(t *testing.T) {
	root := buildPackedStorageFixture(t, "sha1", 4)

	account, err := measureGitStorage(context.Background(), 60*time.Second, root)
	if err != nil {
		t.Fatal(err)
	}
	for _, class := range account.Classes {
		if class.Class != ObjectClassOperationBlob && class.Class != ObjectClassStateBlob {
			continue
		}
		if class.DiskBytes >= class.RawBytes {
			t.Fatalf("class %q on-disk bytes %d are not smaller than raw bytes %d", class.Class, class.DiskBytes, class.RawBytes)
		}
	}
	if account.ReachableDiskBytes >= account.ReachableRawBytes {
		t.Fatalf("reachable on-disk bytes %d are not smaller than raw bytes %d", account.ReachableDiskBytes, account.ReachableRawBytes)
	}
}

// Mutation witness: making the fixture depend on the object format - different
// document contents, different history depth, or a different task population -
// breaks the cross-format logical equivalence this accounting relies on.
func TestStorageFixturesCarryEquivalentLogicalDataAcrossObjectFormats(t *testing.T) {
	if !supportsObjectFormat(t, "sha256") {
		testenv.MissingCapability(t, "Git does not support SHA-256 repositories")
	}
	const operations = 4

	sha1Root := buildPackedStorageFixture(t, "sha1", operations)
	sha256Root := buildPackedStorageFixture(t, "sha256", operations)

	if got := strings.TrimSpace(runGit(t, sha1Root, "rev-parse", "--show-object-format")); got != "sha1" {
		t.Fatalf("first repository object format = %q", got)
	}
	if got := strings.TrimSpace(runGit(t, sha256Root, "rev-parse", "--show-object-format")); got != "sha256" {
		t.Fatalf("second repository object format = %q", got)
	}

	if first, second := taskRefCommitCounts(t, sha1Root), taskRefCommitCounts(t, sha256Root); !reflect.DeepEqual(first, second) {
		t.Fatalf("per-task commit counts differ across object formats:\n%v\n%v", first, second)
	}
	for _, document := range []string{"operation.json", "state.json"} {
		first := documentContentDigest(t, sha1Root, document)
		second := documentContentDigest(t, sha256Root, document)
		if first != second {
			t.Fatalf("%s content digests differ across object formats: %s vs %s", document, first, second)
		}
	}

	sha1Account, err := measureGitStorage(context.Background(), 60*time.Second, sha1Root)
	if err != nil {
		t.Fatal(err)
	}
	sha256Account, err := measureGitStorage(context.Background(), 60*time.Second, sha256Root)
	if err != nil {
		t.Fatal(err)
	}
	if sha1Account.ReachableObjects != sha256Account.ReachableObjects {
		t.Fatalf("reachable objects sha1 %d, sha256 %d", sha1Account.ReachableObjects, sha256Account.ReachableObjects)
	}
	sha1Classes := classAccountsByName(sha1Account)
	sha256Classes := classAccountsByName(sha256Account)
	for _, class := range []string{ObjectClassCommit, ObjectClassTree, ObjectClassOperationBlob, ObjectClassStateBlob} {
		if sha1Classes[class].Objects != sha256Classes[class].Objects {
			t.Fatalf("class %q object counts differ: sha1 %d, sha256 %d", class, sha1Classes[class].Objects, sha256Classes[class].Objects)
		}
	}
	for _, class := range []string{ObjectClassOperationBlob, ObjectClassStateBlob} {
		if sha1Classes[class].RawBytes != sha256Classes[class].RawBytes {
			t.Fatalf("class %q raw bytes differ: sha1 %d, sha256 %d", class, sha1Classes[class].RawBytes, sha256Classes[class].RawBytes)
		}
	}
	if sha256Classes[ObjectClassTree].RawBytes <= sha1Classes[ObjectClassTree].RawBytes {
		t.Fatalf("sha256 tree raw bytes %d are not larger than sha1 tree raw bytes %d despite wider object IDs",
			sha256Classes[ObjectClassTree].RawBytes, sha1Classes[ObjectClassTree].RawBytes)
	}
}

// Mutation witness: omitting a depth, losing fixture metadata, folding the two
// disposable caches together, or dropping either measured resource command
// leaves the report unable to answer the story's questions.
func TestMeasureStorageResourcesReportsEveryComponentForEachDepth(t *testing.T) {
	binary := buildWorkbookBinary(t)
	root := t.TempDir()

	report, err := MeasureStorageResources(context.Background(), StorageResourceSpec{
		WorkbookBinary:  binary,
		Root:            root,
		Fixture:         smallStorageFixtureSpec("sha1", 0),
		OperationDepths: []int{5, 3},
		CommandTimeout:  120 * time.Second,
		FixtureTimeout:  120 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	if report.MaxResidentRawUnit != MaxResidentUnitForOS(runtime.GOOS) {
		t.Fatalf("report max resident unit = %q", report.MaxResidentRawUnit)
	}
	if report.Platform == "" || report.ObjectSizeSemantics == "" || report.RepositoryState == "" {
		t.Fatalf("report metadata = %#v", report)
	}
	if len(report.Depths) != 2 {
		t.Fatalf("report has %d depths, want 2", len(report.Depths))
	}
	if report.Depths[0].Fixture.OperationsPerTask != 3 || report.Depths[1].Fixture.OperationsPerTask != 5 {
		t.Fatalf("depths are not ordered by operations per task: %d then %d",
			report.Depths[0].Fixture.OperationsPerTask, report.Depths[1].Fixture.OperationsPerTask)
	}

	for _, depth := range report.Depths {
		label := strconv.Itoa(depth.Fixture.OperationsPerTask)
		if depth.Fixture.TotalTasks != 6 || depth.Fixture.ActiveTasks != 5 ||
			depth.Fixture.TombstonedTasks != 1 || depth.Fixture.ObjectFormat != "sha1" {
			t.Fatalf("depth %s fixture metadata = %#v", label, depth.Fixture)
		}
		if depth.Git.ReachableObjects == 0 || depth.Git.UnclassifiedObjects != 0 ||
			depth.Git.ClassifiedObjects != depth.Git.ReachableObjects {
			t.Fatalf("depth %s git accounting = %#v", label, depth.Git)
		}
		if depth.Git.PackFileBytes <= 0 {
			t.Fatalf("depth %s pack bytes = %d", label, depth.Git.PackFileBytes)
		}
		if depth.DisposableCache.ProjectionBytes <= 0 {
			t.Fatalf("depth %s projection cache bytes = %d", label, depth.DisposableCache.ProjectionBytes)
		}
		if depth.DisposableCache.ValidationBytes <= 0 {
			t.Fatalf("depth %s validation cache bytes = %d", label, depth.DisposableCache.ValidationBytes)
		}
		wantTotal := depth.DisposableCache.ProjectionBytes + depth.DisposableCache.ProjectionSidecarBytes +
			depth.DisposableCache.ValidationBytes + depth.DisposableCache.ValidationSidecarBytes
		if depth.DisposableCache.TotalBytes != wantTotal {
			t.Fatalf("depth %s disposable cache total = %d, want %d", label, depth.DisposableCache.TotalBytes, wantTotal)
		}
		if depth.DisposableCache.ProjectionPath != filepath.Join(".git", "workbook", "cache.sqlite") ||
			depth.DisposableCache.ValidationPath != filepath.Join(".git", "workbook", "validation.sqlite") {
			t.Fatalf("depth %s cache paths = %q and %q", label, depth.DisposableCache.ProjectionPath, depth.DisposableCache.ValidationPath)
		}

		if len(depth.Resources) != 2 {
			t.Fatalf("depth %s has %d resource measurements, want 2", label, len(depth.Resources))
		}
		if depth.Resources[0].Command != ResourceCommandProjectionRebuild ||
			depth.Resources[1].Command != ResourceCommandFullValidation {
			t.Fatalf("depth %s resource commands = %q and %q", label, depth.Resources[0].Command, depth.Resources[1].Command)
		}
		for _, resource := range depth.Resources {
			if resource.ExitCode != 0 || resource.TimedOut || resource.Error != "" {
				t.Fatalf("depth %s %s = exit %d timedOut %t error %q", label, resource.Command, resource.ExitCode, resource.TimedOut, resource.Error)
			}
			if resource.MaxResidentBytes <= 0 {
				t.Fatalf("depth %s %s peak resident bytes = %d", label, resource.Command, resource.MaxResidentBytes)
			}
			if resource.Milliseconds <= 0 {
				t.Fatalf("depth %s %s elapsed = %f ms", label, resource.Command, resource.Milliseconds)
			}
			if len(resource.Argv) == 0 {
				t.Fatalf("depth %s %s recorded no argv", label, resource.Command)
			}
		}
		if depth.Resources[0].RepositoryBytesDelta <= 0 {
			t.Fatalf("depth %s projection rebuild wrote %d repository bytes, want a positive durable delta",
				label, depth.Resources[0].RepositoryBytesDelta)
		}
	}

	shallow, deep := report.Depths[0], report.Depths[1]
	if deep.Git.ReachableObjects <= shallow.Git.ReachableObjects {
		t.Fatalf("deeper fixture reachable objects %d not greater than shallow %d", deep.Git.ReachableObjects, shallow.Git.ReachableObjects)
	}
	if classAccountsByName(deep.Git)[ObjectClassCommit].Objects != int64(6*5) ||
		classAccountsByName(shallow.Git)[ObjectClassCommit].Objects != int64(6*3) {
		t.Fatalf("commit counts do not follow the requested depths")
	}
}

func TestMeasureStorageResourcesRejectsIncompleteSpecs(t *testing.T) {
	base := StorageResourceSpec{
		WorkbookBinary:  "workbook",
		Root:            t.TempDir(),
		Fixture:         smallStorageFixtureSpec("sha1", 0),
		OperationDepths: []int{3},
		CommandTimeout:  time.Second,
		FixtureTimeout:  time.Second,
	}
	for name, mutate := range map[string]func(*StorageResourceSpec){
		"missing binary":    func(spec *StorageResourceSpec) { spec.WorkbookBinary = "" },
		"missing root":      func(spec *StorageResourceSpec) { spec.Root = "" },
		"no depths":         func(spec *StorageResourceSpec) { spec.OperationDepths = nil },
		"duplicate depth":   func(spec *StorageResourceSpec) { spec.OperationDepths = []int{3, 3} },
		"zero depth":        func(spec *StorageResourceSpec) { spec.OperationDepths = []int{0} },
		"no command budget": func(spec *StorageResourceSpec) { spec.CommandTimeout = 0 },
		"no fixture budget": func(spec *StorageResourceSpec) { spec.FixtureTimeout = 0 },
	} {
		spec := base
		mutate(&spec)
		if _, err := MeasureStorageResources(context.Background(), spec); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
}

// Mutation witness: renaming, reordering, or dropping any reported field
// silently breaks every consumer of the machine-readable report.
func TestStorageResourceReportJSONFieldsAreStable(t *testing.T) {
	report := sampleStorageResourceReport()

	first, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	second, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatalf("repeated encodings differ:\n%s\n%s", first, second)
	}

	want := []string{
		"blockIoCountersSupported",
		"depths[].disposableCache.projectionBytes",
		"depths[].disposableCache.projectionPath",
		"depths[].disposableCache.projectionSidecarBytes",
		"depths[].disposableCache.totalBytes",
		"depths[].disposableCache.validationBytes",
		"depths[].disposableCache.validationPath",
		"depths[].disposableCache.validationSidecarBytes",
		"depths[].fixture.activeTasks",
		"depths[].fixture.objectFormat",
		"depths[].fixture.operationsPerTask",
		"depths[].fixture.tombstonedTasks",
		"depths[].fixture.totalTasks",
		"depths[].git.classes[].class",
		"depths[].git.classes[].diskBytes",
		"depths[].git.classes[].objects",
		"depths[].git.classes[].rawBytes",
		"depths[].git.classifiedObjects",
		"depths[].git.looseObjectFileBytes",
		"depths[].git.looseObjects",
		"depths[].git.objectDirectoryBytes",
		"depths[].git.objectFormat",
		"depths[].git.packAuxiliaryBytes",
		"depths[].git.packFileBytes",
		"depths[].git.packIndexBytes",
		"depths[].git.packedObjects",
		"depths[].git.packs",
		"depths[].git.reachableDiskBytes",
		"depths[].git.reachableObjects",
		"depths[].git.reachableRawBytes",
		"depths[].git.refPrefix",
		"depths[].git.taskRefs",
		"depths[].git.unclassifiedObjects",
		"depths[].git.workbookRefs",
		"depths[].resources[].argv",
		"depths[].resources[].blockInputOperations",
		"depths[].resources[].blockIoCountersSupported",
		"depths[].resources[].blockOutputOperations",
		"depths[].resources[].command",
		"depths[].resources[].exitCode",
		"depths[].resources[].involuntaryContextSwitches",
		"depths[].resources[].majorPageFaults",
		"depths[].resources[].maxResidentBytes",
		"depths[].resources[].maxResidentRaw",
		"depths[].resources[].maxResidentRawUnit",
		"depths[].resources[].milliseconds",
		"depths[].resources[].minorPageFaults",
		"depths[].resources[].repositoryBytesDelta",
		"depths[].resources[].systemMilliseconds",
		"depths[].resources[].timedOut",
		"depths[].resources[].userMilliseconds",
		"depths[].resources[].voluntaryContextSwitches",
		"maxResidentRawUnit",
		"objectSizeSemantics",
		"platform",
		"repositoryState",
	}
	got := jsonFieldPaths(t, first)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("JSON field paths mismatch\n got: %v\nwant: %v", got, want)
	}
}

// Mutation witness: dropping any storage component, peak resident memory, or
// I/O column from the generated Markdown leaves the human-readable evidence
// incomplete for a fixture depth.
func TestStorageResourceMarkdownReportsEveryComponent(t *testing.T) {
	var buffer strings.Builder
	report := Report{
		Format:           ReportFormat,
		Version:          ReportVersion,
		Phase:            "acceptance",
		StorageResources: sampleStorageResourceReport(),
	}
	if err := report.WriteMarkdown(&buffer); err != nil {
		t.Fatal(err)
	}
	markdown := buffer.String()
	for _, fragment := range []string{
		"## Storage and peak resources",
		"operation-blob",
		"state-blob",
		"tree",
		"commit",
		"Pack files",
		"Pack indexes",
		"Loose objects",
		"SQLite projection",
		"Validation cache",
		"Peak resident",
		"Block input",
		"Block output",
		"Major page faults",
		"projection-rebuild",
		"full-validation",
		"6 tasks by 3 operations",
		"kilobytes",
	} {
		if !strings.Contains(markdown, fragment) {
			t.Fatalf("Markdown is missing %q:\n%s", fragment, markdown)
		}
	}
}

func TestReportOmitsStorageSectionWhenNotMeasured(t *testing.T) {
	report := Report{Format: ReportFormat, Version: ReportVersion, Phase: "baseline"}
	var jsonBuffer, markdownBuffer strings.Builder
	if err := report.WriteJSON(&jsonBuffer); err != nil {
		t.Fatal(err)
	}
	if err := report.WriteMarkdown(&markdownBuffer); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(jsonBuffer.String(), "storageResources") {
		t.Fatalf("JSON report carries an empty storage section:\n%s", jsonBuffer.String())
	}
	if strings.Contains(markdownBuffer.String(), "Storage and peak resources") {
		t.Fatalf("Markdown report carries an empty storage section:\n%s", markdownBuffer.String())
	}
}

func sampleStorageResourceReport() *StorageResourceReport {
	return &StorageResourceReport{
		Platform:                 "linux/amd64",
		MaxResidentRawUnit:       MaxResidentUnitKilobytes,
		BlockIOCountersSupported: true,
		ObjectSizeSemantics:      "rawBytes is uncompressed content; diskBytes is the packed representation",
		RepositoryState:          "packed refs and git gc --prune=now",
		Depths: []StorageResourceDepth{{
			Fixture: FixtureSpec{TotalTasks: 6, ActiveTasks: 5, TombstonedTasks: 1, OperationsPerTask: 3, ObjectFormat: "sha1"},
			Git: GitStorageAccount{
				ObjectFormat:        "sha1",
				RefPrefix:           workbookRefPrefix,
				WorkbookRefs:        6,
				TaskRefs:            6,
				ReachableObjects:    72,
				ClassifiedObjects:   72,
				UnclassifiedObjects: 0,
				Classes: []ObjectClassAccount{
					{Class: ObjectClassOperationBlob, Objects: 18, RawBytes: 9000, DiskBytes: 3000},
					{Class: ObjectClassStateBlob, Objects: 18, RawBytes: 8000, DiskBytes: 2500},
					{Class: ObjectClassOtherBlob, Objects: 0},
					{Class: ObjectClassTree, Objects: 18, RawBytes: 1800, DiskBytes: 900},
					{Class: ObjectClassCommit, Objects: 18, RawBytes: 4000, DiskBytes: 1500},
					{Class: ObjectClassAnnotatedTag, Objects: 0},
				},
				ReachableRawBytes:    22800,
				ReachableDiskBytes:   7900,
				Packs:                1,
				PackedObjects:        72,
				PackFileBytes:        8100,
				PackIndexBytes:       1400,
				PackAuxiliaryBytes:   100,
				LooseObjects:         0,
				LooseObjectFileBytes: 0,
				ObjectDirectoryBytes: 9600,
			},
			DisposableCache: DisposableCacheAccount{
				ProjectionPath:         filepath.Join(".git", "workbook", "cache.sqlite"),
				ProjectionBytes:        40960,
				ProjectionSidecarBytes: 0,
				ValidationPath:         filepath.Join(".git", "workbook", "validation.sqlite"),
				ValidationBytes:        20480,
				ValidationSidecarBytes: 0,
				TotalBytes:             61440,
			},
			Resources: []ResourceMeasurement{
				{
					Command: ResourceCommandProjectionRebuild, Argv: []string{"rebuild", "--json"},
					Milliseconds: 120.5, MaxResidentBytes: 41943040, MaxResidentRaw: 40960,
					MaxResidentRawUnit: MaxResidentUnitKilobytes, UserMilliseconds: 80, SystemMilliseconds: 30,
					BlockInputOperations: 8, BlockOutputOperations: 96, BlockIOCountersSupported: true,
					MinorPageFaults: 5000, MajorPageFaults: 3, VoluntaryContextSwitches: 40,
					InvoluntaryContextSwitches: 9, RepositoryBytesDelta: 40960,
				},
				{
					Command: ResourceCommandFullValidation, Argv: []string{"validate", "--full", "--json"},
					Milliseconds: 300.25, MaxResidentBytes: 62914560, MaxResidentRaw: 61440,
					MaxResidentRawUnit: MaxResidentUnitKilobytes, UserMilliseconds: 200, SystemMilliseconds: 70,
					BlockInputOperations: 12, BlockOutputOperations: 48, BlockIOCountersSupported: true,
					MinorPageFaults: 9000, MajorPageFaults: 5, VoluntaryContextSwitches: 60,
					InvoluntaryContextSwitches: 11, RepositoryBytesDelta: 20480,
				},
			},
		}},
	}
}

func jsonFieldPaths(t *testing.T, encoded []byte) []string {
	t.Helper()
	var decoded any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	paths := make(map[string]struct{})
	var walk func(prefix string, value any)
	walk = func(prefix string, value any) {
		switch typed := value.(type) {
		case map[string]any:
			for key, nested := range typed {
				path := key
				if prefix != "" {
					path = prefix + "." + key
				}
				switch nested.(type) {
				case map[string]any, []any:
					walk(path, nested)
				default:
					paths[path] = struct{}{}
				}
			}
		case []any:
			if len(typed) == 0 {
				paths[prefix] = struct{}{}
				return
			}
			for _, element := range typed {
				switch element.(type) {
				case map[string]any, []any:
					walk(prefix+"[]", element)
				default:
					paths[prefix] = struct{}{}
				}
			}
		default:
			paths[prefix] = struct{}{}
		}
	}
	walk("", decoded)
	names := make([]string, 0, len(paths))
	for path := range paths {
		names = append(names, path)
	}
	sort.Strings(names)
	return names
}

func classAccountsByName(account GitStorageAccount) map[string]ObjectClassAccount {
	byName := make(map[string]ObjectClassAccount, len(account.Classes))
	for _, class := range account.Classes {
		byName[class.Class] = class
	}
	return byName
}

func taskRefCommitCounts(t *testing.T, root string) map[string]int {
	t.Helper()
	counts := make(map[string]int)
	for _, ref := range strings.Split(strings.TrimSpace(runGit(t, root, "for-each-ref", "--format=%(refname)", workbookTaskRefPrefix)), "\n") {
		if ref == "" {
			continue
		}
		count, err := strconv.Atoi(strings.TrimSpace(runGit(t, root, "rev-list", "--count", ref)))
		if err != nil {
			t.Fatal(err)
		}
		counts[ref] = count
	}
	if len(counts) == 0 {
		t.Fatal("repository has no Workbook task refs")
	}
	return counts
}

// documentContentDigest hashes the sorted contents of every blob stored under
// the named path, so two repositories can be compared independently of the
// object IDs their hash algorithm produces.
func documentContentDigest(t *testing.T, root, path string) string {
	t.Helper()
	refs := strings.TrimSpace(runGit(t, root, "for-each-ref", "--format=%(objectname)", workbookRefPrefix))
	objects := gitWithInput(t, root, []byte(refs+"\n"), "rev-list", "--objects", "--stdin")

	var contents []string
	for _, line := range strings.Split(strings.TrimSpace(objects), "\n") {
		oid, name, found := strings.Cut(line, " ")
		if !found || name != path {
			continue
		}
		contents = append(contents, runGit(t, root, "cat-file", "blob", oid))
	}
	if len(contents) == 0 {
		t.Fatalf("no %s blobs reachable from Workbook refs", path)
	}
	sort.Strings(contents)
	digest := sha256.New()
	for _, content := range contents {
		fmt.Fprintf(digest, "%d\n", len(content))
		digest.Write([]byte(content))
	}
	return strconv.Itoa(len(contents)) + ":" + hex.EncodeToString(digest.Sum(nil))
}

func gitWithInput(t *testing.T, root string, input []byte, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	command.Stdin = strings.NewReader(string(input))
	output, err := command.Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return string(output)
}

func writeStorageStubBinary(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "workbook")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func storageCommandNamed(t *testing.T, name string) storageResourceCommand {
	t.Helper()
	for _, command := range storageResourceCommands() {
		if command.name == name {
			return command
		}
	}
	t.Fatalf("no measured storage command named %q", name)
	return storageResourceCommand{}
}

// Mutation witness: checking only the exit code lets a peak-memory number be
// attributed to a command that audited nothing, which is exactly the number the
// storage evidence exists to report.
func TestMeasureStorageResourceCommandChecksResultContentNotOnlyTheExitCode(t *testing.T) {
	const operations = 4
	fixture := smallStorageFixtureSpec("sha1", operations)
	commits := fixture.TotalTasks * operations
	truthful := fmt.Sprintf(
		`printf '%%s\n' '{"format":"workbook.result","version":1,"command":"validate","data":{"validatorVersion":1,"full":true,"taskCount":%d,"tasksChecked":%d,"commitsChecked":%d,"cacheHits":0,"valid":%d,"invalid":0,"pending":0,"cachePath":"","failures":[]}}'`,
		fixture.TotalTasks, fixture.TotalTasks, commits, fixture.TotalTasks,
	)
	auditedNothing := `printf '%s\n' '{"format":"workbook.result","version":1,"command":"validate","data":{"validatorVersion":1,"full":true,"taskCount":0,"tasksChecked":0,"commitsChecked":0,"cacheHits":0,"valid":0,"invalid":0,"pending":0,"cachePath":"","failures":[]}}'`
	rebuiltNothing := `printf '%s\n' '{"format":"workbook.result","version":1,"command":"rebuild","data":{"taskCount":0,"cachePath":""}}'`

	for _, test := range []struct {
		name    string
		command string
		body    string
		want    string
	}{
		{name: "complete audit", command: ResourceCommandFullValidation, body: truthful},
		{name: "audited nothing", command: ResourceCommandFullValidation, body: auditedNothing, want: "literal oracle"},
		{name: "rebuilt nothing", command: ResourceCommandProjectionRebuild, body: rebuiltNothing, want: "rebuild task count = 0"},
	} {
		t.Run(test.name, func(t *testing.T) {
			measurement, err := measureStorageResourceCommand(
				context.Background(),
				StorageResourceSpec{WorkbookBinary: writeStorageStubBinary(t, test.body), CommandTimeout: 30 * time.Second},
				t.TempDir(),
				fixture,
				storageCommandNamed(t, test.command),
			)
			if test.want == "" {
				if err != nil {
					t.Fatalf("measureStorageResourceCommand() error = %v, want the truthful result accepted", err)
				}
				if measurement.Stdout != nil || measurement.Stderr != nil {
					t.Fatalf("measurement retained %d stdout and %d stderr bytes", len(measurement.Stdout), len(measurement.Stderr))
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("measureStorageResourceCommand() error = %v, want a rejection containing %q", err, test.want)
			}
		})
	}
}
