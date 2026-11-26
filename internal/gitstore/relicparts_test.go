package gitstore

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/dgoings/workbook/internal/core"
	"github.com/dgoings/workbook/internal/testrepo"
)

// loseRelicObject deletes one object from the repository, as a damaged or
// partly copied object store is missing it.
func loseRelicObject(t *testing.T, repo *Repository, objectID string) {
	t.Helper()
	gitDir := gitOutput(t, repo, "rev-parse", "--absolute-git-dir")
	if err := os.Remove(filepath.Join(gitDir, "objects", objectID[:2], objectID[2:])); err != nil {
		t.Fatalf("remove object %s to lose it: %v", objectID, err)
	}
}

// assertNoRawGitText asserts that a refusal about a relic is in Workbook's own
// words: it names the relic and repeats nothing a Git command printed.
func assertNoRawGitText(t *testing.T, label string, err error, relicID string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: no error", label)
	}
	message := err.Error()
	if !strings.Contains(message, relicID) {
		t.Fatalf("%s: error %q does not name relic %s", label, message, relicID)
	}
	for _, raw := range []string{"ls-tree", "rev-list", "exit status", "fatal:", "error:", "git "} {
		if strings.Contains(message, raw) {
			t.Fatalf("%s: error %q repeats Git's own text (%q)", label, message, raw)
		}
	}
}

// A relic commit can name a tree Git does not have: one part's directory, or
// the directory of all parts. Git cannot list such a commit's tree. That is
// the relic's damage, not a failure to run Git, so it is corrupt data for
// that relic and for no other.
func TestAMissingPartTreeIsCorruptDataForTheRelic(t *testing.T) {
	t.Parallel()
	forBothRelicFormats(t, func(t *testing.T, repo *Repository, config core.ProjectConfig) {
		ctx := context.Background()
		healthy := writeRelicRootNumbered(t, repo, config, 2, relicTestPartNumbered(1).withBody("md", []byte("sound\n")))
		tip, a, _ := relicRuleHistory(t, repo, config)
		relicID := tip.Operation.RelicID
		absent := strings.Repeat("0", len(tip.Head)-1) + "1"

		onePart := replaceRelicTreeEntry(t,
			relicLines(gitOutput(t, repo, "ls-tree", tip.Head+":parts")), a.id, "040000 tree "+absent+"\t"+a.id)
		forgedParts := gitOutputWithInput(t, repo, []byte(strings.Join(onePart, "\n")+"\n"), "mktree", "--missing")
		top := relicLines(gitOutput(t, repo, "ls-tree", tip.Head))
		for name, entry := range map[string]string{
			"one part's directory":       "040000 tree " + forgedParts + "\tparts",
			"the directory of all parts": "040000 tree " + absent + "\tparts",
		} {
			tree := gitOutputWithInput(t, repo,
				[]byte(strings.Join(replaceRelicTreeEntry(t, top, "parts", entry), "\n")+"\n"), "mktree", "--missing")
			forged := forgeRelicCommitOf(t, repo, tree, relicParents(t, repo, tip.Head)...)
			pointRelicRef(t, repo, relicID, forged)

			checked, failure, err := repo.ValidateRelicHistory(ctx, config, core.RelicHead{RelicID: relicID, ObjectID: forged})
			if err != nil {
				t.Fatalf("%s: ValidateRelicHistory() error = %v, want the relic's failure", name, err)
			}
			if failure == nil || failure.Commit != forged || checked != 1 {
				t.Fatalf("%s: ValidateRelicHistory() = %d checked, failure %+v; want 1 checked and commit %s refused",
					name, checked, failure, forged)
			}
			assertRelicCategory(t, failure.Err, core.CategoryCorruptData, forged)
			assertNoRawGitText(t, name+" (validate)", failure.Err, relicID)

			_, err = repo.GetRelic(ctx, config, relicID)
			assertGetRelicRefused(t, err)
			assertNoRawGitText(t, name+" (read)", err, relicID)
		}

		// The relic beside it is untouched by any of this.
		assertRelicHistoryValid(t, repo, config, healthy.Operation.RelicID, healthy.Head, 1)
		if _, err := repo.GetRelic(ctx, config, healthy.Operation.RelicID); err != nil {
			t.Fatalf("GetRelic() of the healthy relic error = %v", err)
		}
	})
}

// A history whose commits are not all in the object store cannot be walked.
// Validation says which commit names a parent Git does not have. A read of
// the newest commit looks no further than that commit's parent.
func TestAMissingCommitIsCorruptDataForTheRelic(t *testing.T) {
	t.Parallel()
	for _, missing := range []string{"the first commit", "a commit in the middle", "the newest commit"} {
		t.Run(missing, func(t *testing.T) {
			t.Parallel()
			forBothRelicFormats(t, func(t *testing.T, repo *Repository, config core.ProjectConfig) {
				ctx := context.Background()
				healthy := writeRelicRootNumbered(t, repo, config, 2, relicTestPartNumbered(1).withBody("md", []byte("sound\n")))
				part := relicTestPartNumbered(1).withBody("md", []byte("the first text\n"))
				root := writeRelicRootNumbered(t, repo, config, 1, part)
				relicID := root.Operation.RelicID
				second := writeRelicChild(t, repo, config, root, nil, relicTitleSet(700001, "A second title"))
				third := writeRelicChild(t, repo, config, second, nil, relicTitleSet(700002, "A third title"))
				fourth := writeRelicChild(t, repo, config, third, nil, relicTitleSet(700003, "A fourth title"))
				assertRelicHistoryValid(t, repo, config, relicID, fourth.Head, 4)

				// lost is the commit removed, and named is the commit the
				// failure is reported at: the oldest one Git still has above
				// the gap, or the ref's own target when that is what is gone.
				lost, named, readable := root.Head, second.Head, true
				switch missing {
				case "a commit in the middle":
					lost, named = second.Head, third.Head
				case "the newest commit":
					lost, named, readable = fourth.Head, fourth.Head, false
				}
				loseRelicObject(t, repo, lost)

				checked, failure, err := repo.ValidateRelicHistory(ctx, config, core.RelicHead{RelicID: relicID, ObjectID: fourth.Head})
				if err != nil {
					t.Fatalf("ValidateRelicHistory() error = %v, want the relic's failure", err)
				}
				if failure == nil || failure.Commit != named || checked != 0 {
					t.Fatalf("ValidateRelicHistory() = %d checked, failure %+v; want none checked and commit %s named",
						checked, failure, named)
				}
				assertRelicCategory(t, failure.Err, core.CategoryCorruptData, lost)
				assertNoRawGitText(t, "validate", failure.Err, relicID)

				got, err := repo.GetRelic(ctx, config, relicID)
				if readable {
					if err != nil || got.Head != fourth.Head {
						t.Fatalf("GetRelic() = %s, %v; want %s: a single read looks no further than the parent", got.Head, err, fourth.Head)
					}
				} else {
					assertGetRelicRefused(t, err)
					assertNoRawGitText(t, "read", err, relicID)
				}

				// A ref whose commit's own parent is gone is refused on read.
				if missing == "the first commit" {
					pointRelicRef(t, repo, relicID, second.Head)
					_, err := repo.GetRelic(ctx, config, relicID)
					assertGetRelicRefused(t, err)
					assertNoRawGitText(t, "read of the commit above the gap", err, relicID)
				}

				heads, err := repo.ListRelicHeads(ctx, config)
				if err != nil || len(heads) != 2 {
					t.Fatalf("ListRelicHeads() = %v, %v; want both relics listed", heads, err)
				}
				assertRelicHistoryValid(t, repo, config, healthy.Operation.RelicID, healthy.Head, 1)
			})
		})
	}
}

// Whatever stops one relic's newest commit from being read, the error says
// which relic it was.
func TestAnUnreadableRelicNamesItself(t *testing.T) {
	t.Parallel()
	repo, config := relicRepository(t, testrepo.FormatSHA1)
	ctx := context.Background()
	healthy := writeRelicRootNumbered(t, repo, config, 2, relicTestPartNumbered(1).withBody("md", []byte("sound\n")))
	root := writeRelicRootNumbered(t, repo, config, 1, relicTestPartNumbered(1).withBody("md", []byte("the first text\n")))
	relicID := root.Operation.RelicID
	pack := relicChildPack(root, relicTitleSet(700001, "A new title"))
	record := foldRelic(t, &root, pack)
	state := encodedRelicDocument(t, record.State)
	parts := forgedPartsOf(t, repo, root.Head)

	for name, documents := range map[string][2][]byte{
		"a state with a space after it":      {encodedRelicDocument(t, pack), []byte(strings.TrimSuffix(string(state), "\n") + " \n")},
		"a state that is not JSON":           {encodedRelicDocument(t, pack), []byte("{\n")},
		"a pack with a member nobody knows":  {bytes.Replace(encodedRelicDocument(t, pack), []byte(`{"format"`), []byte(`{"extra":1,"format"`), 1), state},
		"a state that claims another title":  {encodedRelicDocument(t, pack), bytes.Replace(state, []byte("A new title"), []byte("Another one"), 1)},
		"a state whose title is not trimmed": {encodedRelicDocument(t, pack), bytes.Replace(state, []byte("A new title"), []byte("A new title "), 1)},
	} {
		forged := forgeRelicCommit(t, repo, root.Head, documents[0], documents[1], parts)
		pointRelicRef(t, repo, relicID, forged)
		wantPrefix := "relic " + relicID + ": "

		_, err := repo.GetRelic(ctx, config, relicID)
		assertGetRelicRefused(t, err)
		if !strings.HasPrefix(err.Error(), wantPrefix) {
			t.Fatalf("%s: GetRelic() error %q does not start with %q", name, err, wantPrefix)
		}

		heads, err := repo.ListRelicHeads(ctx, config)
		if err != nil || len(heads) != 2 {
			t.Fatalf("%s: ListRelicHeads() = %v, %v", name, heads, err)
		}
		_, err = repo.ReadRelicTips(ctx, config, heads)
		if name == "a state that claims another title" {
			// The tip read does not fold, so it serves this one.
			if err != nil {
				t.Fatalf("%s: ReadRelicTips() error = %v", name, err)
			}
		} else {
			assertRelicCategory(t, err, core.CategoryCorruptData)
			if !strings.HasPrefix(err.Error(), wantPrefix) || strings.Contains(err.Error(), healthy.Operation.RelicID) {
				t.Fatalf("%s: ReadRelicTips() error %q must start with %q and not name the healthy relic", name, err, wantPrefix)
			}
		}

		_, failure, err := repo.ValidateRelicHistory(ctx, config, core.RelicHead{RelicID: relicID, ObjectID: forged})
		if err != nil || failure == nil || !strings.HasPrefix(failure.Err.Error(), wantPrefix) {
			t.Fatalf("%s: ValidateRelicHistory() = %+v, %v; want a failure that starts with %q", name, failure, err, wantPrefix)
		}
		if strings.Count(failure.Err.Error(), relicID) != 1 {
			t.Fatalf("%s: the failure names the relic more than once: %q", name, failure.Err)
		}
	}
}

// relicObjectSize returns the size Git reports for one object.
func relicObjectSize(t *testing.T, repo *Repository, name string) int64 {
	t.Helper()
	size, err := strconv.ParseInt(gitOutput(t, repo, "cat-file", "-s", name), 10, 64)
	if err != nil {
		t.Fatalf("cat-file -s %s: %v", name, err)
	}
	return size
}

// observeRelicObjects records every object a relic reader asks Git for with
// its content.
func observeRelicObjects(repo *Repository) *[]string {
	requests := &[]string{}
	repo.relicObjectObserver = func(request string) { *requests = append(*requests, request) }
	return requests
}

// relicCommitRequests is what reading one relic commit's documents asks for.
func relicCommitRequests(commit string) []string {
	return []string{commit, commit + "^{tree}", commit + ":operation.json", commit + ":state.json", commit + ":links.json"}
}

// A commit's parts are read from the trees themselves: the parts tree, each
// part's subtree, and the size of each blob. Nothing lists the commit's tree.
func TestRelicPartObjectsAreReadWithoutListingATree(t *testing.T) {
	t.Parallel()
	forBothRelicFormats(t, func(t *testing.T, repo *Repository, config core.ProjectConfig) {
		ctx := context.Background()
		parts := []relicTestPart{
			relicTestPartNumbered(1).withField("heading", "Goals").withBody("md", []byte("the goals\n")),
			relicTestPartNumbered(2).withBody("txt", []byte("plain text, and rather more of it\n")),
			relicTestPartNumbered(3).withField("status", "open"),
		}
		root := writeRelicRoot(t, repo, config, parts...)

		commands := observeCommands(repo)
		commit, err := repo.ReadRelicCommit(ctx, config, root.Operation.RelicID, root.Head)
		ran := *commands
		repo.commandObserver = nil
		if err != nil {
			t.Fatalf("ReadRelicCommit() error = %v", err)
		}
		for _, command := range ran {
			if len(command) > 0 && command[0] == "ls-tree" {
				t.Fatalf("ReadRelicCommit() listed a tree: %q", ran)
			}
		}
		// The documents, the parts tree and the subtrees are read; the blobs
		// are only measured.
		if batches, checks := countCommand(ran, "cat-file", "--batch"), countCommand(ran, "cat-file", "--batch-check"); batches != 3 || checks != 1 || len(ran) != 4 {
			t.Fatalf("ReadRelicCommit() ran %q, want three cat-file --batch and one cat-file --batch-check", ran)
		}

		want := make(map[string]RelicPartObjects, len(parts))
		for _, part := range parts {
			path := root.Head + ":parts/" + part.id
			objects := RelicPartObjects{
				Tree:        gitOutput(t, repo, "rev-parse", path),
				Fields:      gitOutput(t, repo, "rev-parse", path+"/fields.json"),
				FieldsBytes: relicObjectSize(t, repo, path+"/fields.json"),
			}
			if part.hasBody {
				objects.Body = gitOutput(t, repo, "rev-parse", path+"/body")
				objects.BodyBytes = relicObjectSize(t, repo, path+"/body")
			}
			want[part.id] = objects
		}
		if !maps.Equal(commit.Parts, want) {
			t.Fatalf("commit parts = %+v, want %+v", commit.Parts, want)
		}
	})
}

// Whoever can push can write a parts tree of any size. A reader knows from the
// relic's own state how large that tree is, to the byte, and refuses any other
// size from Git's header: the tree is not read, and nothing it names is asked
// for.
func TestAnOversizePartsTreeIsRefusedBeforeItIsRead(t *testing.T) {
	t.Parallel()
	forBothRelicFormats(t, func(t *testing.T, repo *Repository, config core.ProjectConfig) {
		ctx := context.Background()
		part := relicTestPartNumbered(1).withBody("md", []byte("the goals\n"))
		root := writeRelicRoot(t, repo, config, part)
		relicID := root.Operation.RelicID
		subtree := gitOutput(t, repo, "rev-parse", root.Head+":parts/"+part.id)
		honestBytes := relicObjectSize(t, repo, root.Head+":parts")

		// The root commit again, with a parts tree that holds the one part the
		// state lists and extra entries beside it. Every extra entry names the
		// same subtree, so the forgery costs one object however many there are.
		crowd := func(extra int) (commit string, treeBytes int64) {
			t.Helper()
			entries := []string{"040000 tree " + subtree + "\t" + part.id}
			for index := range extra {
				entries = append(entries, "040000 tree "+subtree+"\t"+relicTestID(100000+index))
			}
			crowded := forgeRelicTree(t, repo, entries)
			top := replaceRelicTreeEntry(t,
				relicLines(gitOutput(t, repo, "ls-tree", root.Head)), "parts", "040000 tree "+crowded+"\tparts")
			return forgeRelicCommitOf(t, repo, forgeRelicTree(t, repo, top)), relicObjectSize(t, repo, crowded)
		}

		for _, extra := range []int{5000, 1} {
			forged, crowdedBytes := crowd(extra)
			requests := observeRelicObjects(repo)
			commands := observeCommands(repo)
			_, err := repo.ReadRelicCommit(ctx, config, relicID, forged)
			ran, asked := *commands, *requests
			repo.commandObserver, repo.relicObjectObserver = nil, nil

			assertRelicCategory(t, err, core.CategoryCorruptData, relicID,
				fmt.Sprintf("%d bytes", crowdedBytes), fmt.Sprintf("%d bytes", honestBytes))
			if len(err.Error()) > 500 {
				t.Fatalf("%d extra entries: the refusal is %d bytes long; it must not carry what the tree holds", extra, len(err.Error()))
			}
			if want := append(relicCommitRequests(forged), forged+":parts"); !slices.Equal(asked, want) {
				t.Fatalf("%d extra entries: the read asked for %q, want only %q: no part subtree", extra, asked, want)
			}
			if countCommand(ran, "cat-file", "--batch") != 2 || len(ran) != 2 {
				t.Fatalf("%d extra entries: the read ran %q, want the documents and the parts tree and nothing after", extra, ran)
			}

			checked, failure, err := repo.ValidateRelicHistory(ctx, config, core.RelicHead{RelicID: relicID, ObjectID: forged})
			if err != nil || failure == nil || failure.Commit != forged || checked != 0 {
				t.Fatalf("%d extra entries: ValidateRelicHistory() = %d checked, failure %+v, error %v; want commit %s refused",
					extra, checked, failure, err, forged)
			}
			assertRelicCategory(t, failure.Err, core.CategoryCorruptData, fmt.Sprintf("%d bytes", crowdedBytes))
		}

		// The honest commit is read, subtree and all.
		requests := observeRelicObjects(repo)
		if _, err := repo.ReadRelicCommit(ctx, config, relicID, root.Head); err != nil {
			t.Fatalf("ReadRelicCommit(honest) error = %v", err)
		}
		if !slices.Contains(*requests, subtree) {
			t.Fatalf("the honest read asked for %q, want the part's subtree %s among them", *requests, subtree)
		}
	})
}

// A part's subtree holds fields.json and at most a body, so its size has a
// ceiling a reader can work out before asking for it. A larger one is refused
// from Git's header, and no blob it names is asked about.
func TestAnOversizePartSubtreeIsRefusedBeforeItIsRead(t *testing.T) {
	t.Parallel()
	forBothRelicFormats(t, func(t *testing.T, repo *Repository, config core.ProjectConfig) {
		ctx := context.Background()
		part := relicTestPartNumbered(1).withBody("md", []byte("the goals\n"))
		root := writeRelicRoot(t, repo, config, part)
		relicID := root.Operation.RelicID
		path := root.Head + ":parts/" + part.id
		fields, body := gitOutput(t, repo, "rev-parse", path+"/fields.json"), gitOutput(t, repo, "rev-parse", path+"/body")
		// The honest subtree holds both entries, so it is the largest a part's
		// subtree may be.
		largestBytes := relicObjectSize(t, repo, path)

		entries := []string{"100644 blob " + fields + "\tfields.json", "100644 blob " + body + "\tbody"}
		for index := range 5000 {
			entries = append(entries, fmt.Sprintf("100644 blob %s\tnote-%04d", body, index))
		}
		crowded := forgeRelicTree(t, repo, entries)
		crowdedBytes := relicObjectSize(t, repo, crowded)
		parts := forgeRelicTree(t, repo, []string{"040000 tree " + crowded + "\t" + part.id})
		top := replaceRelicTreeEntry(t,
			relicLines(gitOutput(t, repo, "ls-tree", root.Head)), "parts", "040000 tree "+parts+"\tparts")
		forged := forgeRelicCommitOf(t, repo, forgeRelicTree(t, repo, top))

		commands := observeCommands(repo)
		_, err := repo.ReadRelicCommit(ctx, config, relicID, forged)
		ran := *commands
		repo.commandObserver = nil
		assertRelicCategory(t, err, core.CategoryCorruptData, relicID, part.id,
			fmt.Sprintf("%d bytes", crowdedBytes), fmt.Sprintf("%d bytes", largestBytes))
		if len(err.Error()) > 500 {
			t.Fatalf("the refusal is %d bytes long; it must not carry what the tree holds", len(err.Error()))
		}
		if countCommand(ran, "cat-file", "--batch") != 3 || len(ran) != 3 {
			t.Fatalf("the read ran %q, want the documents, the parts tree and the subtree, and no blob measured", ran)
		}
	})
}

// However many commits are read together, Git is started the same number of
// times: once for every commit's documents, once for every parts tree, once for
// every part's subtree and once to measure every blob.
func TestReadingManyRelicCommitsUsesAFixedNumberOfGitProcesses(t *testing.T) {
	t.Parallel()
	repo, config := relicRepository(t, testrepo.FormatSHA1)
	ctx := context.Background()
	const relics = 20
	heads := make([]core.RelicHead, relics)
	for number := range relics {
		tip := writeRelicRootNumbered(t, repo, config, number+1,
			relicTestPartNumbered(1).withField("heading", fmt.Sprintf("Section %d", number)).
				withBody("md", []byte(fmt.Sprintf("Body of relic %d.\n", number))),
			relicTestPartNumbered(2).withField("status", "open"))
		heads[number] = core.RelicHead{RelicID: tip.Operation.RelicID, ObjectID: tip.Head}
	}
	bare := writeRelicRootNumbered(t, repo, config, relics+1)

	commands := observeCommands(repo)
	read := func(heads []core.RelicHead, wantParts int) int {
		t.Helper()
		*commands = nil
		commits, errs := repo.readRelicCommitsPartial(ctx, config, heads)
		if len(commits) != len(heads) || len(errs) != len(heads) {
			t.Fatalf("readRelicCommitsPartial() returned %d commits and %d errors for %d heads", len(commits), len(errs), len(heads))
		}
		for index, head := range heads {
			if errs[index] != nil {
				t.Fatalf("readRelicCommitsPartial() error for %s = %v", head.RelicID, errs[index])
			}
			if commits[index].ObjectID != head.ObjectID || commits[index].Record.State.RelicID != head.RelicID ||
				len(commits[index].Parts) != wantParts {
				t.Fatalf("commit %d = %s of relic %s with %d parts, want %s of relic %s with %d parts", index,
					commits[index].ObjectID, commits[index].Record.State.RelicID, len(commits[index].Parts),
					head.ObjectID, head.RelicID, wantParts)
			}
		}
		return len(*commands)
	}

	one, many := read(heads[:1], 2), read(heads, 2)
	if one != many || many != 4 {
		t.Fatalf("reading 1 commit ran %d Git commands and reading %d ran %d, want 4 for both: %q", one, relics, many, *commands)
	}
	// A pass with nothing to ask starts no process: a relic with no part has
	// no parts tree, and no heads means nothing to read at all.
	if got := read([]core.RelicHead{{RelicID: bare.Operation.RelicID, ObjectID: bare.Head}}, 0); got != 1 {
		t.Fatalf("reading a relic with no part ran %d Git commands, want 1: %q", got, *commands)
	}
	if got := read(nil, 0); got != 0 {
		t.Fatalf("reading no commit ran %d Git commands, want none: %q", got, *commands)
	}
}

// Commits read together fail apart. Whatever is wrong with one of them, the
// others are returned, and its error names its own relic.
func TestOneUnreadableRelicCommitDoesNotFailTheBatch(t *testing.T) {
	t.Parallel()
	repo, config := relicRepository(t, testrepo.FormatSHA1)
	ctx := context.Background()
	first := writeRelicRootNumbered(t, repo, config, 1, relicTestPartNumbered(1).withBody("md", []byte("the first\n")))
	second := writeRelicRootNumbered(t, repo, config, 2, relicTestPartNumbered(1).withBody("md", []byte("the second\n")))
	third := writeRelicRootNumbered(t, repo, config, 3, relicTestPartNumbered(1).withField("status", "open"))
	secondID := second.Operation.RelicID
	stored := forgedPartsOf(t, repo, second.Head)
	added := relicTestID(3)
	body := []byte("a new part\n")
	subtree := gitOutput(t, repo, "rev-parse", second.Head+":parts/"+relicTestID(1))
	crowded := forgeRelicTree(t, repo, []string{
		"040000 tree " + subtree + "\t" + relicTestID(1), "040000 tree " + subtree + "\t" + relicTestID(9),
	})

	for name, test := range map[string]struct {
		objectID  string
		category  core.Category
		fragments []string
	}{
		"a part without fields.json": {
			forgeRelicChild(t, repo, second, relicChildPack(second, relicPartAdd(added, "2/1", body)),
				withForgedPart(stored, added, forgedPart{body: body, hasBody: true})),
			core.CategoryCorruptData, []string{added, "fields.json"},
		},
		"a parts tree with an entry too many": {
			forgeRelicCommitOf(t, repo, forgeRelicTree(t, repo, replaceRelicTreeEntry(t,
				relicLines(gitOutput(t, repo, "ls-tree", second.Head)), "parts", "040000 tree "+crowded+"\tparts"))),
			core.CategoryCorruptData, []string{"bytes"},
		},
		"a part's subtree Git does not have": {
			forgeRelicCommitOf(t, repo, gitOutputWithInput(t, repo, []byte(strings.Join(replaceRelicTreeEntry(t,
				relicLines(gitOutput(t, repo, "ls-tree", second.Head)), "parts",
				"040000 tree "+gitOutputWithInput(t, repo,
					[]byte("040000 tree "+strings.Repeat("0", len(second.Head)-1)+"1\t"+relicTestID(1)+"\n"), "mktree", "--missing")+
					"\tparts"), "\n")+"\n"), "mktree", "--missing")),
			core.CategoryCorruptData, []string{"missing from Git"},
		},
		"a commit Git does not have": {
			strings.Repeat("0", len(second.Head)-1) + "1", core.CategoryCorruptData, nil,
		},
		"another relic's commit": {third.Head, core.CategoryCorruptData, nil},
		"not an object ID":       {"not-an-object-id", core.CategoryValidation, nil},
	} {
		heads := []core.RelicHead{
			{RelicID: first.Operation.RelicID, ObjectID: first.Head},
			{RelicID: secondID, ObjectID: test.objectID},
			{RelicID: third.Operation.RelicID, ObjectID: third.Head},
		}
		commits, errs := repo.readRelicCommitsPartial(ctx, config, heads)
		if len(commits) != 3 || len(errs) != 3 {
			t.Fatalf("%s: readRelicCommitsPartial() returned %d commits and %d errors for 3 heads", name, len(commits), len(errs))
		}
		if errs[0] != nil || errs[2] != nil {
			t.Fatalf("%s: the commits beside the unreadable one failed: %v, %v", name, errs[0], errs[2])
		}
		if commits[0].ObjectID != first.Head || len(commits[0].Parts) != 1 || commits[2].ObjectID != third.Head || len(commits[2].Parts) != 1 {
			t.Fatalf("%s: the commits beside the unreadable one = %+v and %+v, want %s and %s with their parts",
				name, commits[0], commits[2], first.Head, third.Head)
		}
		if errs[1] == nil {
			t.Fatalf("%s: the unreadable commit was read: %+v", name, commits[1])
		}
		assertRelicCategory(t, errs[1], test.category, append([]string{secondID}, test.fragments...)...)
		if strings.Contains(errs[1].Error(), first.Operation.RelicID) {
			t.Fatalf("%s: the error names a relic that was read: %v", name, errs[1])
		}
		if commits[1].ObjectID != "" || commits[1].Parts != nil {
			t.Fatalf("%s: the unreadable commit was returned all the same: %+v", name, commits[1])
		}
	}
}
