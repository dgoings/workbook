package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/dgoings/workbook/internal/core"
)

// createThreadTask is a task to comment on and attach to. It carries no
// description, so every line these tests read out of `show` belongs to the
// thread rather than to prose that happens to mention one.
func createThreadTask(t *testing.T, repository string) core.Task {
	t.Helper()
	code, stdout, stderr := run(t, repository, "create", "Ship it", "--no-sync", "--json")
	if code != 0 {
		t.Fatalf("create code = %d, want 0; stderr = %q", code, stderr)
	}
	return decodeMutationTask(t, stdout, "create")
}

func showTask(t *testing.T, repository, id string) core.Task {
	t.Helper()
	code, stdout, stderr := run(t, repository, "show", id, "--json")
	if code != 0 {
		t.Fatalf("show --json code = %d, want 0; stderr = %q", code, stderr)
	}
	var task core.Task
	if err := json.Unmarshal(assertJSONResult(t, stdout, "show").Data, &task); err != nil {
		t.Fatalf("decode shown task: %v", err)
	}
	return task
}

func writeAttachmentFile(t *testing.T, name string, content []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func TestUpdateAddsEditsAndRemovesAComment(t *testing.T) {
	// Mutation caught: dropping --edit-comment's body, or reading --comment
	// beside --edit-comment as a second comment to add rather than as the new
	// body — either of which leaves the original text on the task.
	repository := initializedRepository(t)
	task := createThreadTask(t, repository)

	code, stdout, stderr := run(t, repository, "update", task.ID, "--comment", "shipped it", "--no-sync")
	if code != 0 {
		t.Fatalf("update --comment code = %d, want 0; stderr = %q", code, stderr)
	}
	if want := "\tComment:\t+shipped it\n"; !strings.Contains(stdout, want) {
		t.Fatalf("update output = %q, want it to name the comment it added %q", stdout, want)
	}

	added := showTask(t, repository, task.ID)
	if len(added.Comments) != 1 {
		t.Fatalf("comments = %#v, want exactly one", added.Comments)
	}
	comment := added.Comments[0]
	if comment.Body != "shipped it" || comment.Author == "" || comment.Edited() {
		t.Fatalf("comment = %#v, want the body, an author, and no edit", comment)
	}

	// The identifier may be typed as a prefix, exactly as a task ID may be.
	code, stdout, stderr = run(
		t, repository, "update", task.ID,
		"--edit-comment", comment.ID[:12], "--comment", "shipped it, twice", "--no-sync",
	)
	if code != 0 {
		t.Fatalf("update --edit-comment code = %d, want 0; stderr = %q", code, stderr)
	}
	if want := "\tComment:\tshipped it → shipped it, twice\n"; !strings.Contains(stdout, want) {
		t.Fatalf("edit output = %q, want it to name both bodies %q", stdout, want)
	}

	edited := showTask(t, repository, task.ID)
	if len(edited.Comments) != 1 {
		t.Fatalf("comments after edit = %#v, want exactly one", edited.Comments)
	}
	if edited.Comments[0].ID != comment.ID || edited.Comments[0].Body != "shipped it, twice" {
		t.Fatalf("edited comment = %#v, want the same comment with the new body", edited.Comments[0])
	}
	if !edited.Comments[0].Edited() {
		t.Fatal("edited comment reports no edit time, want one")
	}

	// The change log summarizes one commit and does not replay the thread to
	// recover the previous body, so it reports the new one and says it replaced
	// something. What it must not print is the bare arrow that an empty
	// left-hand side produces, which reads as a renderer that lost a value.
	code, stdout, stderr = run(t, repository, "show", task.ID, "--history")
	if code != 0 {
		t.Fatalf("show --history code = %d, want 0; stderr = %q", code, stderr)
	}
	if want := "\tComment:\tshipped it, twice\t(edited)\n"; !strings.Contains(stdout, want) {
		t.Fatalf("history = %q, want the edit row %q", stdout, want)
	}
	for _, line := range strings.Split(stdout, "\n") {
		if strings.Contains(line, "\t → ") {
			t.Fatalf("history line %q renders an arrow with nothing before it", line)
		}
	}

	code, stdout, stderr = run(t, repository, "update", task.ID, "--remove-comment", comment.ID, "--no-sync", "--json")
	if code != 0 {
		t.Fatalf("update --remove-comment code = %d, want 0; stderr = %q", code, stderr)
	}
	if removed := decodeMutationTask(t, stdout, "update"); len(removed.Comments) != 0 {
		t.Fatalf("comments after removal = %#v, want none", removed.Comments)
	}
}

func TestUpdateAttachesAndRemovesFilesAndLinks(t *testing.T) {
	// Mutation caught: deriving the attachment's name or media type from
	// something other than the file it was read from, or letting --attach-label
	// land on the file rather than on the link.
	repository := initializedRepository(t)
	task := createThreadTask(t, repository)
	path := writeAttachmentFile(t, "notes.md", []byte("# Notes\n"))

	code, stdout, stderr := run(t, repository, "update", task.ID, "--attach-file", path, "--no-sync")
	if code != 0 {
		t.Fatalf("update --attach-file code = %d, want 0; stderr = %q", code, stderr)
	}
	if want := "\tAttachment:\t+notes.md\n"; !strings.Contains(stdout, want) {
		t.Fatalf("attach output = %q, want it to name the attachment %q", stdout, want)
	}

	code, _, stderr = run(
		t, repository, "update", task.ID,
		"--attach-url", "https://example.com/pr/1", "--attach-label", "The pull request", "--no-sync",
	)
	if code != 0 {
		t.Fatalf("update --attach-url code = %d, want 0; stderr = %q", code, stderr)
	}

	attached := showTask(t, repository, task.ID)
	if len(attached.Attachments) != 2 {
		t.Fatalf("attachments = %#v, want two", attached.Attachments)
	}
	var file, link core.Attachment
	for _, attachment := range attached.Attachments {
		if attachment.Kind == core.AttachmentFile {
			file = attachment
		} else {
			link = attachment
		}
	}
	if file.Name != "notes.md" || file.Media != "text/markdown" || file.Size != int64(len("# Notes\n")) {
		t.Fatalf("file attachment = %#v, want the file's name, derived media type, and size", file)
	}
	if file.Blob == "" {
		t.Fatalf("file attachment = %#v, want a blob object ID", file)
	}
	if link.URL != "https://example.com/pr/1" || link.Label != "The pull request" {
		t.Fatalf("link attachment = %#v, want the URL and its label", link)
	}
	if link.Size != 0 || link.Blob != "" || link.Name != "" {
		t.Fatalf("link attachment = %#v, want no file data", link)
	}

	code, stdout, stderr = run(
		t, repository, "update", task.ID, "--remove-attachment", file.ID[:12], "--no-sync",
	)
	if code != 0 {
		t.Fatalf("update --remove-attachment code = %d, want 0; stderr = %q", code, stderr)
	}
	if want := "\tAttachment:\t-notes.md\n"; !strings.Contains(stdout, want) {
		t.Fatalf("removal output = %q, want it to name what it removed %q", stdout, want)
	}
	remaining := showTask(t, repository, task.ID)
	if len(remaining.Attachments) != 1 || remaining.Attachments[0].ID != link.ID {
		t.Fatalf("attachments after removal = %#v, want only the link", remaining.Attachments)
	}
}

func TestUpdateThreadFlagsRefuseInvocationsWithNoMeaning(t *testing.T) {
	// Mutation caught: reading a flag whose partner is missing as the intent it
	// resembles. --edit-comment without a body has no new text, and an
	// --edit-comment whose value is empty would reach core as an addition and
	// silently write a new comment instead of editing one.
	repository := initializedRepository(t)
	task := createThreadTask(t, repository)
	run(t, repository, "update", task.ID, "--comment", "first", "--no-sync")
	comment := showTask(t, repository, task.ID).Comments[0]

	for _, test := range []struct {
		name    string
		args    []string
		message string
	}{
		{
			name:    "edit without a body",
			args:    []string{"--edit-comment", comment.ID},
			message: "update --edit-comment requires --comment for the new body",
		},
		{
			name:    "edit without a comment",
			args:    []string{"--edit-comment", "", "--comment", "text"},
			message: "update --edit-comment requires a comment ID",
		},
		{
			name:    "label without a link",
			args:    []string{"--attach-label", "The pull request"},
			message: "update --attach-label requires --attach-url",
		},
		{
			name:    "attach nothing",
			args:    []string{"--attach-file", ""},
			message: "update --attach-file requires a path",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			args := append([]string{"update", task.ID}, test.args...)
			code, stdout, stderr := run(t, repository, append(args, "--no-sync")...)
			if code != 2 {
				t.Fatalf("code = %d, want 2; stdout = %q, stderr = %q", code, stdout, stderr)
			}
			assertHumanError(t, stderr, test.message)

			code, stdout, stderr = run(t, repository, append(args, "--no-sync", "--json")...)
			if code != 2 {
				t.Fatalf("JSON code = %d, want 2; stdout = %q, stderr = %q", code, stdout, stderr)
			}
			if stdout != "" {
				t.Fatalf("stdout = %q, want nothing written", stdout)
			}
			assertJSONError(t, stderr, core.CategoryInvocation, test.message)
		})
	}

	// The refusals above are the whole matrix of combinations with no meaning:
	// every other pairing is two intents, and composes.
	unchanged := showTask(t, repository, task.ID)
	if len(unchanged.Comments) != 1 || unchanged.Comments[0].Body != "first" {
		t.Fatalf("comments = %#v, want the refusals to have written nothing", unchanged.Comments)
	}
}

func TestUpdateThreadFlagsCompose(t *testing.T) {
	// Mutation caught: refusing a --comment beside --remove-comment as
	// ambiguous. Removing one remark and writing another is two intents, and
	// the pair belongs in one pack rather than in two commits.
	repository := initializedRepository(t)
	task := createThreadTask(t, repository)
	run(t, repository, "update", task.ID, "--comment", "first", "--no-sync")
	comment := showTask(t, repository, task.ID).Comments[0]

	code, _, stderr := run(
		t, repository, "update", task.ID,
		"--remove-comment", comment.ID, "--comment", "second", "--no-sync",
	)
	if code != 0 {
		t.Fatalf("compose code = %d, want 0; stderr = %q", code, stderr)
	}
	comments := showTask(t, repository, task.ID).Comments
	if len(comments) != 1 || comments[0].Body != "second" {
		t.Fatalf("comments = %#v, want the first removed and the second written", comments)
	}
}

func TestUpdateRefusesBlankAndMissingThreadTargets(t *testing.T) {
	// Mutation caught: letting an identifier nobody holds reach the pack, or
	// resolving a prefix that names two things by picking one. Both are refused
	// before anything is written, and each reports the category its caller can
	// act on.
	repository := initializedRepository(t)
	task := createThreadTask(t, repository)
	first := writeAttachmentFile(t, "one.txt", []byte("one"))
	second := writeAttachmentFile(t, "two.txt", []byte("two"))
	run(t, repository, "update", task.ID, "--attach-file", first, "--attach-file", second, "--no-sync")
	attachments := showTask(t, repository, task.ID).Attachments
	if len(attachments) != 1 {
		t.Fatalf("attachments = %#v, want the last --attach-file to win as every repeated flag does", attachments)
	}
	run(t, repository, "update", task.ID, "--attach-file", first, "--no-sync")

	for _, test := range []struct {
		name     string
		args     []string
		code     int
		category core.Category
		message  string
	}{
		{
			name:     "no such comment",
			args:     []string{"--remove-comment", "01JZZZZZZZZZZZZZZZZZZZZZZZ"},
			code:     4,
			category: core.CategoryNotFound,
			message:  `no comment matches "01JZZZZZZZZZZZZZZZZZZZZZZZ"`,
		},
		{
			// A blank identifier core reads correctly is left to core, which is
			// the mutation boundary and owns the refusal. Only a blank one core
			// would misread — an --edit-comment that would arrive as an
			// addition — is refused at the flags, above.
			name:     "blank comment ID",
			args:     []string{"--remove-comment", ""},
			code:     5,
			category: core.CategoryValidation,
			message:  "removing a comment requires its ID",
		},
		{
			name:     "blank comment body",
			args:     []string{"--comment", "   "},
			code:     5,
			category: core.CategoryValidation,
			message:  "comment body must not be blank",
		},
		{
			name:     "link that is not a link",
			args:     []string{"--attach-url", "javascript:alert(1)"},
			code:     5,
			category: core.CategoryValidation,
			message:  "attachment URL must use http or https",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			args := append([]string{"update", task.ID}, test.args...)
			code, _, stderr := run(t, repository, append(args, "--no-sync", "--json")...)
			if code != test.code {
				t.Fatalf("code = %d, want %d; stderr = %q", code, test.code, stderr)
			}
			assertJSONError(t, stderr, test.category, test.message)
		})
	}

	// A ULID opens with ten characters of millisecond timestamp, so two
	// attachments added seconds apart share the first several of them — which
	// is exactly why `show` prints identifiers whole: a shared prefix is
	// refused rather than resolved to whichever one came first.
	ambiguous := showTask(t, repository, task.ID).Attachments[0].ID[:4]
	code, _, stderr := run(
		t, repository, "update", task.ID, "--remove-attachment", ambiguous, "--no-sync", "--json",
	)
	if code != 5 {
		t.Fatalf("ambiguous prefix code = %d, want 5; stderr = %q", code, stderr)
	}
	assertJSONError(t, stderr, core.CategoryValidation, `attachment ID prefix "`+ambiguous+`" is ambiguous`)
}

func TestUpdateWritesOnePackForFieldsCommentsAndAttachments(t *testing.T) {
	// Mutation caught: sending the thread intents through a second write, which
	// would give one invocation two commits, two change-log entries, and two
	// chances to half-succeed.
	repository := initializedRepository(t)
	task := createThreadTask(t, repository)
	path := writeAttachmentFile(t, "notes.txt", []byte("notes\n"))
	ref := "refs/workbook/tasks/" + task.ID
	before := gitOutput(t, repository, "rev-list", "--count", ref)

	code, stdout, stderr := run(
		t, repository, "update", task.ID,
		"--status", "done", "--comment", "shipped", "--attach-file", path,
		"--attach-url", "https://example.com/pr/1", "--no-sync",
	)
	if code != 0 {
		t.Fatalf("composed update code = %d, want 0; stderr = %q", code, stderr)
	}
	for _, want := range []string{
		"\tComment:\t+shipped\n",
		"\tAttachment:\t+notes.txt\n",
		"\tAttachment:\t+https://example.com/pr/1\n",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("composed update output = %q, want %q", stdout, want)
		}
	}

	after := gitOutput(t, repository, "rev-list", "--count", ref)
	if before != "1" || after != "2" {
		t.Fatalf("commit count went %s → %s, want exactly one new commit", before, after)
	}

	code, stdout, stderr = run(t, repository, "show", task.ID, "--history")
	if code != 0 {
		t.Fatalf("show --history code = %d, want 0; stderr = %q", code, stderr)
	}
	if got := strings.Count(stdout, "changed status, comment, and attachment"); got != 1 {
		t.Fatalf("history = %q, want exactly one row naming all three changes", stdout)
	}
	for _, want := range []string{
		"\tStatus:\tbacklog → done\n",
		"\tComment:\t+shipped\n",
		"\tAttachment:\t+notes.txt\n",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("history = %q, want %q under the one row", stdout, want)
		}
	}
}

func TestShowRendersTheThreadAndTheAttachmentList(t *testing.T) {
	// Mutation caught: leaving the thread out of the text rendering, where it
	// is the only place a reader learns the identifiers the update flags take.
	repository := initializedRepository(t)
	task := createThreadTask(t, repository)
	path := writeAttachmentFile(t, "notes.txt", []byte("six!!\n"))
	run(t, repository, "update", task.ID, "--comment", "first line\n\nsecond line", "--no-sync")
	run(t, repository, "update", task.ID, "--attach-file", path, "--no-sync")
	run(t, repository, "update", task.ID, "--attach-url", "https://example.com/pr/1", "--no-sync")
	shown := showTask(t, repository, task.ID)
	comment := shown.Comments[0]

	code, stdout, stderr := run(t, repository, "show", task.ID)
	if code != 0 {
		t.Fatalf("show code = %d, want 0; stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "\nComments:\n") || !strings.Contains(stdout, "\nAttachments:\n") {
		t.Fatalf("show output = %q, want both section headers", stdout)
	}
	if want := "\t" + comment.ID + "\t" + comment.Author + "\t"; !strings.Contains(stdout, want) {
		t.Fatalf("show output = %q, want the comment attributed with %q", stdout, want)
	}
	// The body is indented one tab deeper than the attribution line, so no line
	// of it can be read as another comment's attribution.
	if want := "\t\tfirst line\n\n\t\tsecond line\n"; !strings.Contains(stdout, want) {
		t.Fatalf("show output = %q, want the body block %q", stdout, want)
	}
	for _, attachment := range shown.Attachments {
		want := "\t" + attachment.ID + "\t"
		if attachment.Kind == core.AttachmentFile {
			want += "notes.txt\tfile\t6 bytes\n"
		} else {
			want += "\tlink\thttps://example.com/pr/1\n"
		}
		if !strings.Contains(stdout, want) {
			t.Fatalf("show output = %q, want the attachment row %q", stdout, want)
		}
	}

	// An edited comment says so, and says when.
	run(t, repository, "update", task.ID, "--edit-comment", comment.ID, "--comment", "revised", "--no-sync")
	code, stdout, _ = run(t, repository, "show", task.ID)
	if code != 0 {
		t.Fatalf("show after edit code = %d, want 0", code)
	}
	if !strings.Contains(stdout, "\t(edited ") {
		t.Fatalf("show output = %q, want the edited marker", stdout)
	}
}

func TestShowLeavesATaskWithoutAThreadUnchanged(t *testing.T) {
	// Mutation caught: printing empty Comments and Attachments sections, which
	// would change what `show` prints for every task written before this
	// existed.
	repository := initializedRepository(t)
	task := createThreadTask(t, repository)

	code, stdout, stderr := run(t, repository, "show", task.ID)
	if code != 0 {
		t.Fatalf("show code = %d, want 0; stderr = %q", code, stderr)
	}
	if strings.Contains(stdout, "Comments:") || strings.Contains(stdout, "Attachments:") {
		t.Fatalf("show output = %q, want no thread sections at all", stdout)
	}
	if !strings.HasSuffix(stdout, "Head:\t"+task.Head+"\n") {
		t.Fatalf("show output = %q, want it to end at the head line", stdout)
	}
}

func TestShowJSONCarriesTheThread(t *testing.T) {
	// Mutation caught: the thread reaching the text rendering while the
	// envelope every machine consumer reads stays empty.
	repository := initializedRepository(t)
	task := createThreadTask(t, repository)
	path := writeAttachmentFile(t, "notes.txt", []byte("notes\n"))
	run(t, repository, "update", task.ID, "--comment", "shipped", "--attach-file", path, "--no-sync")

	shown := showTask(t, repository, task.ID)
	if len(shown.Comments) != 1 || shown.Comments[0].Body != "shipped" {
		t.Fatalf("comments = %#v, want the one that was written", shown.Comments)
	}
	if len(shown.Attachments) != 1 || shown.Attachments[0].Name != "notes.txt" {
		t.Fatalf("attachments = %#v, want the one that was attached", shown.Attachments)
	}

	// A task with neither keeps the members out of the document entirely, which
	// is what makes a task written before comments existed encode as it did.
	plain := createThreadTask(t, repository)
	code, stdout, stderr := run(t, repository, "show", plain.ID, "--json")
	if code != 0 {
		t.Fatalf("show --json code = %d, want 0; stderr = %q", code, stderr)
	}
	members := map[string]json.RawMessage{}
	if err := json.Unmarshal(assertJSONResult(t, stdout, "show").Data, &members); err != nil {
		t.Fatalf("decode show data: %v", err)
	}
	if _, present := members["comments"]; present {
		t.Fatalf("show data = %v, want no comments member on a task with no thread", members)
	}
	if _, present := members["attachments"]; present {
		t.Fatalf("show data = %v, want no attachments member on a task with nothing attached", members)
	}
}

func TestShowGetAttachmentWritesExactlyWhatWasAttached(t *testing.T) {
	// Mutation caught: writing the attachment through anything that transforms
	// bytes. The content is deliberately not text: a NUL, an ESC, and a byte no
	// UTF-8 decoder accepts all survive a byte-exact path and none survive a
	// string one.
	repository := initializedRepository(t)
	task := createThreadTask(t, repository)
	content := []byte{0x00, 0x1b, '[', '2', 'K', 0xff, 0xfe, '\n', 'e', 'n', 'd'}
	path := writeAttachmentFile(t, "capture.bin", content)
	run(t, repository, "update", task.ID, "--attach-file", path, "--no-sync")
	attachment := showTask(t, repository, task.ID).Attachments[0]

	code, stdout, stderr := run(t, repository, "show", task.ID, "--get-attachment", attachment.ID)
	if code != 0 {
		t.Fatalf("--get-attachment code = %d, want 0; stderr = %q", code, stderr)
	}
	if stdout != string(content) {
		t.Fatalf("standard output = %q, want the attached bytes %q", stdout, content)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want nothing beside the bytes", stderr)
	}

	out := filepath.Join(t.TempDir(), "recovered.bin")
	code, stdout, stderr = run(
		t, repository, "show", task.ID, "--get-attachment", attachment.ID[:12], "--out", out,
	)
	if code != 0 {
		t.Fatalf("--out code = %d, want 0; stderr = %q", code, stderr)
	}
	written, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read %s: %v", out, err)
	}
	if string(written) != string(content) {
		t.Fatalf("written file = %q, want the attached bytes %q", written, content)
	}
	if !strings.Contains(stdout, "Wrote:\t"+out+"\tcapture.bin\t11 bytes\n") {
		t.Fatalf("--out output = %q, want it to name the file it wrote", stdout)
	}
}

func TestShowGetAttachmentRefusesWhatHasNoBytes(t *testing.T) {
	// Mutation caught: answering a link with an empty file, or a missing
	// attachment with one. A link's URL is what the caller was reaching for, so
	// the refusal hands it back rather than only naming the kind.
	repository := initializedRepository(t)
	task := createThreadTask(t, repository)
	run(t, repository, "update", task.ID,
		"--attach-url", "https://example.com/pr/1", "--attach-label", "The pull request", "--no-sync")
	link := showTask(t, repository, task.ID).Attachments[0]

	// The refusals are plain text, because --json is refused beside
	// --get-attachment: a caller reading bytes reads the category off the exit
	// code, which is what distinguishes the two refusals below.
	code, stdout, stderr := run(t, repository, "show", task.ID, "--get-attachment", link.ID)
	if code != 5 {
		t.Fatalf("link code = %d, want 5; stderr = %q", code, stderr)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want nothing written", stdout)
	}
	assertHumanError(t, stderr,
		"attachment "+link.ID+" is a link and holds no bytes; it points at https://example.com/pr/1")

	code, stdout, stderr = run(t, repository, "show", task.ID, "--get-attachment", "01JZZZ")
	if code != 4 {
		t.Fatalf("missing code = %d, want 4; stderr = %q", code, stderr)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want nothing written", stdout)
	}
	assertHumanError(t, stderr, `no attachment matches "01JZZZ"`)

	// Nothing is created for the refused write either.
	out := filepath.Join(t.TempDir(), "never.bin")
	code, _, stderr = run(t, repository, "show", task.ID, "--get-attachment", link.ID, "--out", out)
	if code != 5 {
		t.Fatalf("link --out code = %d, want 5; stderr = %q", code, stderr)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("stat %s error = %v, want the file never created", out, err)
	}
}

func TestShowGetAttachmentRefusesEveryOptionThatRendersATask(t *testing.T) {
	// Mutation caught: ignoring --json beside --get-attachment, which would
	// answer a request for a JSON document with raw bytes on the same stream a
	// consumer is parsing.
	repository := initializedRepository(t)
	task := createThreadTask(t, repository)
	path := writeAttachmentFile(t, "notes.txt", []byte("notes\n"))
	run(t, repository, "update", task.ID, "--attach-file", path, "--no-sync")
	attachment := showTask(t, repository, task.ID).Attachments[0]

	for _, test := range []struct {
		name    string
		args    []string
		message string
	}{
		{
			name:    "json",
			args:    []string{"--json"},
			message: "cannot use --json with --get-attachment",
		},
		{
			name:    "history",
			args:    []string{"--history"},
			message: "cannot use --history with --get-attachment",
		},
		{
			name:    "compare",
			args:    []string{"--compare", task.Head, task.Head},
			message: "cannot use --compare with --get-attachment",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			args := append([]string{"show", task.ID, "--get-attachment", attachment.ID}, test.args...)
			code, stdout, stderr := run(t, repository, args...)
			if code != 2 {
				t.Fatalf("code = %d, want 2; stdout = %q, stderr = %q", code, stdout, stderr)
			}
			if stdout != "" {
				t.Fatalf("stdout = %q, want nothing written", stdout)
			}
			if !strings.Contains(stderr, test.message) {
				t.Fatalf("stderr = %q, want %q", stderr, test.message)
			}
		})
	}

	code, _, stderr := run(t, repository, "show", task.ID, "--out", filepath.Join(t.TempDir(), "x"))
	if code != 2 {
		t.Fatalf("--out alone code = %d, want 2; stderr = %q", code, stderr)
	}
	if !strings.Contains(stderr, "show --out requires --get-attachment") {
		t.Fatalf("stderr = %q, want the --out refusal", stderr)
	}
}

func TestUpdateRefusesAnOversizedFileBeforeReadingIt(t *testing.T) {
	// Mutation caught: reading the file into memory and letting core refuse it,
	// which turns a mistyped path at a large file into a large allocation. The
	// refusal names the size and the ceiling, and suggests the link that has no
	// ceiling.
	repository := initializedRepository(t)
	task := createThreadTask(t, repository)
	oversized := make([]byte, core.MaxAttachmentFileBytes+1)
	path := writeAttachmentFile(t, "huge.bin", oversized)

	code, stdout, stderr := run(t, repository, "update", task.ID, "--attach-file", path, "--no-sync", "--json")
	if code != 5 {
		t.Fatalf("oversized code = %d, want 5; stderr = %q", code, stderr)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want nothing written", stdout)
	}
	assertJSONError(t, stderr, core.CategoryValidation, "attached file huge.bin is 1048577 bytes and must not exceed 1048576; attach a link instead")

	if attachments := showTask(t, repository, task.ID).Attachments; len(attachments) != 0 {
		t.Fatalf("attachments = %#v, want none", attachments)
	}

	// A directory is not a file, and says so rather than failing on a read.
	code, _, stderr = run(t, repository, "update", task.ID, "--attach-file", filepath.Dir(path), "--no-sync", "--json")
	if code != 5 {
		t.Fatalf("directory code = %d, want 5; stderr = %q", code, stderr)
	}
	assertJSONError(t, stderr, core.CategoryValidation, "cannot attach "+filepath.Dir(path)+", which is a directory")

	code, _, stderr = run(t, repository, "update", task.ID, "--attach-file", path+".missing", "--no-sync", "--json")
	if code != 4 {
		t.Fatalf("missing file code = %d, want 4; stderr = %q", code, stderr)
	}
	assertJSONError(t, stderr, core.CategoryNotFound, "")
}

func TestUpdateRefusesToAttachAnythingButARegularFile(t *testing.T) {
	// Mutation caught: gating --attach-file on IsDir and the stat's size alone.
	// A ceiling read off a stat bounds only a file whose size a stat describes.
	// A character device reports zero bytes and then reads forever — /dev/zero
	// took a measured 14.4 GB of resident memory in four seconds — and a named
	// pipe reports zero bytes and then blocks until somebody writes to it, so
	// the command never returns at all. Core cannot cover for either: its own
	// ceiling is asked of bytes that are already in memory.
	//
	// The pipe is what this test uses, because it is the case a test can create
	// portably; a device file cannot be made in a temporary directory. Both are
	// refused by the same check, and the pipe covers the worse failure of the
	// two — this test hanging is itself the report that the check is gone.
	repository := initializedRepository(t)
	task := createThreadTask(t, repository)
	pipe := filepath.Join(t.TempDir(), "attachment.fifo")
	if err := syscall.Mkfifo(pipe, 0o600); err != nil {
		t.Fatalf("mkfifo %s: %v", pipe, err)
	}

	code, stdout, stderr := run(t, repository, "update", task.ID, "--attach-file", pipe, "--no-sync", "--json")
	if code != 5 {
		t.Fatalf("named pipe code = %d, want 5; stderr = %q", code, stderr)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want nothing written", stdout)
	}
	assertJSONError(t, stderr, core.CategoryValidation, "cannot attach "+pipe+", which is a named pipe")
	if attachments := showTask(t, repository, task.ID).Attachments; len(attachments) != 0 {
		t.Fatalf("attachments = %#v, want none", attachments)
	}
}

func TestShowGetAttachmentReportsBytesThisCloneDoesNotHave(t *testing.T) {
	// Mutation caught: reading the blob without reporting a missing object as a
	// missing attachment. The checkpoint records an object ID, and a clone can
	// hold the checkpoint without holding the object — a partial clone, a
	// damaged object store — so "the task says there is a file here and this
	// clone cannot produce it" has to be its own answer rather than a decode
	// failure. Removing the loose object is how a test reaches that state
	// without a network.
	repository := initializedRepository(t)
	task := createThreadTask(t, repository)
	path := writeAttachmentFile(t, "notes.txt", []byte("notes\n"))
	run(t, repository, "update", task.ID, "--attach-file", path, "--no-sync")
	attachment := showTask(t, repository, task.ID).Attachments[0]

	object := filepath.Join(repository, ".git", "objects", attachment.Blob[:2], attachment.Blob[2:])
	if _, err := os.Stat(object); err != nil {
		t.Fatalf("stat %s: %v; the attachment blob is expected loose in a fresh repository", object, err)
	}
	if err := os.Remove(object); err != nil {
		t.Fatalf("remove %s: %v", object, err)
	}

	code, stdout, stderr := run(t, repository, "show", task.ID, "--get-attachment", attachment.ID)
	if code != 4 {
		t.Fatalf("missing blob code = %d, want 4; stderr = %q", code, stderr)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want nothing written", stdout)
	}
	assertHumanError(t, stderr, "attachment object "+attachment.Blob+" is not in this clone")
}

// The identifiers a caller types are resolved inside the mutation, after the
// session's fetch, and the two clones here are the only way to say so: every
// other test in this file is one clone with --no-sync, where resolving before
// the fetch and resolving after it are the same thing.
//
// Mutation caught: hoisting the resolution out of the mutation closure, which
// resolves against whatever this clone last saw. Both halves below fail under
// that mutation, in opposite directions — the first because the comment is not
// in the stale view, the second because the stale view is missing the second
// comment that makes the prefix ambiguous.
func TestUpdateResolvesThreadTargetsAgainstTheFetchedThread(t *testing.T) {
	author, editor := cliSyncRepositories(t)
	task := cliCreateTask(t, author, "Shared task")
	if code, _, stderr := run(t, author, "push"); code != 0 {
		t.Fatalf("author push code = %d; stderr = %q", code, stderr)
	}
	if code, _, stderr := run(t, editor, "fetch"); code != 0 {
		t.Fatalf("editor fetch code = %d; stderr = %q", code, stderr)
	}

	// The editor writes a comment it keeps to itself, and the author writes one
	// and publishes it. Neither clone has seen the other's.
	if code, _, stderr := run(t, editor, "update", task.ID, "--comment", "from the editor", "--no-sync"); code != 0 {
		t.Fatalf("editor comment code = %d; stderr = %q", code, stderr)
	}
	if code, _, stderr := run(t, author, "update", task.ID, "--comment", "from the author", "--no-sync"); code != 0 {
		t.Fatalf("author comment code = %d; stderr = %q", code, stderr)
	}
	if code, _, stderr := run(t, author, "push"); code != 0 {
		t.Fatalf("author push code = %d; stderr = %q", code, stderr)
	}
	authorComment := showTask(t, author, task.ID).Comments[0]
	editorComment := showTask(t, editor, task.ID).Comments[0]
	shared := commonPrefix(authorComment.ID, editorComment.ID)
	if len(shared) < 3 || len(shared) >= len(authorComment.ID) {
		t.Fatalf("shared prefix %q of %s and %s is not a usable ambiguous prefix",
			shared, authorComment.ID, editorComment.ID)
	}

	// A prefix that names one comment in the editor's stale view names two once
	// the fetch inside the mutation lands the author's, and is refused.
	code, _, stderr := run(t, editor, "update", task.ID, "--edit-comment", shared, "--comment", "rewritten")
	if code != 5 {
		t.Fatalf("ambiguous-after-fetch code = %d, want 5; stderr = %q", code, stderr)
	}
	assertHumanError(t, stderr, `comment ID prefix "`+shared+`" is ambiguous`)
	for _, comment := range showTask(t, editor, task.ID).Comments {
		if comment.Body == "rewritten" {
			t.Fatalf("comments = %#v, want the refused edit to have written nothing",
				showTask(t, editor, task.ID).Comments)
		}
	}

	// And a comment the editor has never seen can be named the moment the fetch
	// brings it in, which is what the ordering buys.
	if code, _, stderr := run(t, author, "update", task.ID, "--comment", "second from the author"); code != 0 {
		t.Fatalf("author second comment code = %d; stderr = %q", code, stderr)
	}
	var unseen core.Comment
	for _, comment := range showTask(t, author, task.ID).Comments {
		if comment.Body == "second from the author" {
			unseen = comment
		}
	}
	if unseen.ID == "" {
		t.Fatal("author's second comment was not recorded")
	}
	if _, found := findComment(showTask(t, editor, task.ID).Comments, unseen.ID); found {
		t.Fatal("the editor already holds the author's second comment; the test proves nothing")
	}

	code, _, stderr = run(t, editor, "update", task.ID, "--edit-comment", unseen.ID, "--comment", "rewritten")
	if code != 0 {
		t.Fatalf("edit-after-fetch code = %d, want 0; stderr = %q", code, stderr)
	}
	edited, found := findComment(showTask(t, editor, task.ID).Comments, unseen.ID)
	if !found || edited.Body != "rewritten" {
		t.Fatalf("comment %s = %#v, want the editor's new body", unseen.ID, edited)
	}
}

func findComment(comments []core.Comment, id string) (core.Comment, bool) {
	for _, comment := range comments {
		if comment.ID == id {
			return comment, true
		}
	}
	return core.Comment{}, false
}

func commonPrefix(left, right string) string {
	limit := min(len(left), len(right))
	index := 0
	for index < limit && left[index] == right[index] {
		index++
	}
	return left[:index]
}

func TestShowSanitizesThreadTextAndKeepsJSONExact(t *testing.T) {
	// Mutation caught: printing a comment body or an attachment name verbatim.
	// Both are attacker-controlled text on the same terminal every other task
	// field is sanitized for, and a comment is the easiest of all of them to
	// write: no field limit, no vocabulary, and anybody who can fetch can add
	// one.
	repository := initializedRepository(t)
	task := createThreadTask(t, repository)
	body := "ok\x1b[2K\x1b[1G\nHead: deadbeef\nComments:"
	path := writeAttachmentFile(t, "re\x1b[2Kport.txt", []byte("bytes\n"))
	run(t, repository, "update", task.ID, "--comment", body, "--attach-file", path, "--no-sync")

	code, stdout, stderr := run(t, repository, "show", task.ID)
	if code != 0 {
		t.Fatalf("show code = %d, want 0; stderr = %q", code, stderr)
	}
	if strings.ContainsRune(stdout, 0x1b) {
		t.Fatalf("show output = %q, want no ESC bytes", stdout)
	}
	head := showTask(t, repository, task.ID).Head
	var headLines, commentHeaders []string
	for _, line := range strings.Split(stdout, "\n") {
		if strings.HasPrefix(line, "Head") {
			headLines = append(headLines, line)
		}
		if strings.HasPrefix(line, "Comments:") {
			commentHeaders = append(commentHeaders, line)
		}
	}
	if len(headLines) != 1 || headLines[0] != "Head:\t"+head {
		t.Fatalf("head lines = %q, want only the real field", headLines)
	}
	if len(commentHeaders) != 1 {
		t.Fatalf("comment section headers = %q, want exactly one", commentHeaders)
	}
	if want := "\t\tok [2K [1G\n\t\tHead: deadbeef\n\t\tComments:\n"; !strings.Contains(stdout, want) {
		t.Fatalf("show output = %q, want the sanitized body block %q", stdout, want)
	}
	if want := "re [2Kport.txt\tfile\t"; !strings.Contains(stdout, want) {
		t.Fatalf("show output = %q, want the sanitized attachment name %q", stdout, want)
	}

	shown := showTask(t, repository, task.ID)
	if shown.Comments[0].Body != strings.TrimSpace(body) {
		t.Fatalf("JSON body = %q, want the stored bytes %q", shown.Comments[0].Body, body)
	}
	if shown.Attachments[0].Name != "re\x1b[2Kport.txt" {
		t.Fatalf("JSON attachment name = %q, want the stored bytes", shown.Attachments[0].Name)
	}
}

func TestShowNeutralizesBidiControlsInACommentBody(t *testing.T) {
	// Mutation caught: neutralizing Cc but passing Cf through, which leaves
	// U+202E RIGHT-TO-LEFT OVERRIDE in the printed line. A comment is the
	// easiest sink to reach with one, and the terminal renders everything after
	// it in reverse, so "open \u202Egpj.exe" reads as "open exe.jpg".
	repository := initializedRepository(t)
	task := createThreadTask(t, repository)
	body := "open \u202Egpj.exe \u202A\u202B\u202C\u202D \u2066\u2067\u2068\u2069 \u200E\u200F\u061C and \U0001F469\u200D\U0001F4BB stays"
	run(t, repository, "update", task.ID, "--comment", body, "--no-sync")

	code, stdout, stderr := run(t, repository, "show", task.ID)
	if code != 0 {
		t.Fatalf("show code = %d, want 0; stderr = %q", code, stderr)
	}
	for _, control := range []rune{0x202A, 0x202B, 0x202C, 0x202D, 0x202E, 0x2066, 0x2067, 0x2068, 0x2069, 0x200E, 0x200F, 0x061C} {
		if strings.ContainsRune(stdout, control) {
			t.Fatalf("show output = %q, want no bidi control %U", stdout, control)
		}
	}
	if want := "\t\topen gpj.exe and \U0001F469\u200D\U0001F4BB stays\n"; !strings.Contains(stdout, want) {
		t.Fatalf("show output = %q, want the neutralized body line %q", stdout, want)
	}

	shown := showTask(t, repository, task.ID)
	if shown.Comments[0].Body != body {
		t.Fatalf("JSON body = %q, want the stored bytes %q", shown.Comments[0].Body, body)
	}
}

func TestUpdateThreadOutcomeSanitizesWhatItEchoes(t *testing.T) {
	// Mutation caught: the confirmation line echoing a comment body verbatim,
	// which is the same sink createForgedTask covers for a title.
	repository := initializedRepository(t)
	task := createThreadTask(t, repository)

	code, stdout, stderr := run(t, repository, "update", task.ID, "--comment", forgedTitle, "--no-sync")
	if code != 0 {
		t.Fatalf("update code = %d, want 0; stderr = %q", code, stderr)
	}
	if strings.ContainsRune(stdout, 0x1b) {
		t.Fatalf("update output = %q, want no ESC bytes", stdout)
	}
	if !strings.Contains(stdout, "\tComment:\t+benign [2K [1Gwb WB-FAKE00000 [done] Deploy approved\n") {
		t.Fatalf("update output = %q, want the sanitized body", stdout)
	}
}
