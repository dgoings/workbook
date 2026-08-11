package core

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestNormalizeTaskRejectsOversizedFields(t *testing.T) {
	// Production mutation: accepting arbitrary bytes in a task document lets one
	// collaborator publish a task every other clone must read into memory.
	tests := []struct {
		name string
		task TaskData
	}{
		{
			name: "title over the ceiling",
			task: sizedTask(func(task *TaskData) {
				task.Title = strings.Repeat("t", MaxTitleBytes+1)
			}),
		},
		{
			name: "title over the ceiling only after trimming is applied",
			task: sizedTask(func(task *TaskData) {
				task.Title = "  " + strings.Repeat("t", MaxTitleBytes+1) + "  "
			}),
		},
		{
			name: "description over the ceiling",
			task: sizedTask(func(task *TaskData) {
				task.Description = strings.Repeat("d", MaxDescriptionBytes+1)
			}),
		},
		{
			name: "one label over the ceiling",
			task: sizedTask(func(task *TaskData) {
				task.Labels = []string{"ok", strings.Repeat("l", MaxLabelBytes+1)}
			}),
		},
		{
			name: "more labels than the ceiling",
			task: sizedTask(func(task *TaskData) {
				task.Labels = distinctLabels(MaxLabelCount + 1)
			}),
		},
		{
			name: "rank over the ceiling",
			task: sizedTask(func(task *TaskData) {
				task.Rank = oversizedRank(MaxRankBytes + 1)
			}),
		},
		{
			name: "more dependencies than the ceiling",
			task: sizedTask(func(task *TaskData) {
				task.Dependencies = distinctDependencies(MaxDependencyCount + 1)
			}),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NormalizeTask("WB", test.task)
			if got, want := CategoryOf(err), CategoryValidation; got != want {
				t.Fatalf("NormalizeTask() category = %q, want %q; error = %v", got, want, err)
			}
		})
	}
}

func TestNormalizeTaskAcceptsFieldsExactlyAtTheCeiling(t *testing.T) {
	// Production mutation: an off-by-one in a ceiling silently narrows what a
	// team may already have stored, which a patch release must not do.
	task := sizedTask(func(task *TaskData) {
		task.Title = strings.Repeat("t", MaxTitleBytes)
		task.Description = strings.Repeat("d", MaxDescriptionBytes)
		task.Labels = append(distinctLabels(MaxLabelCount-1), strings.Repeat("l", MaxLabelBytes))
		task.Rank = oversizedRank(MaxRankBytes)
		task.Dependencies = distinctDependencies(MaxDependencyCount)
	})

	normalized, err := NormalizeTask("WB", task)
	if err != nil {
		t.Fatalf("NormalizeTask() error = %v", err)
	}
	if len(normalized.Title) != MaxTitleBytes {
		t.Fatalf("NormalizeTask() title bytes = %d, want %d", len(normalized.Title), MaxTitleBytes)
	}
	if len(normalized.Description) != MaxDescriptionBytes {
		t.Fatalf("NormalizeTask() description bytes = %d, want %d", len(normalized.Description), MaxDescriptionBytes)
	}
	if len(normalized.Labels) != MaxLabelCount {
		t.Fatalf("NormalizeTask() labels = %d, want %d", len(normalized.Labels), MaxLabelCount)
	}
	if len(normalized.Rank) != MaxRankBytes {
		t.Fatalf("NormalizeTask() rank bytes = %d, want %d", len(normalized.Rank), MaxRankBytes)
	}
	if len(normalized.Dependencies) != MaxDependencyCount {
		t.Fatalf("NormalizeTask() dependencies = %d, want %d", len(normalized.Dependencies), MaxDependencyCount)
	}
}

// TestNormalizeTaskRefusesAnAbsurdRankWithoutParsingIt pins the ceiling ahead of
// the arbitrary-precision parse rather than after it.
//
// A ceiling checked alongside the other field sizes would still reject this
// rank, so rejection alone proves nothing: parseRank runs first inside
// NormalizeTask, and decimal conversion of a digit string that long is where
// the seconds go. Both assertions are about that ordering. The deadline is two
// orders of magnitude above a length comparison and one below the measured cost
// of parsing this rank, and the error must not quote the value back, because
// formatting a rejected megabyte reproduces the cost the ceiling withholds.
func TestNormalizeTaskRefusesAnAbsurdRankWithoutParsingIt(t *testing.T) {
	task := sizedTask(func(task *TaskData) { task.Rank = oversizedRank(4_000_002) })

	start := time.Now()
	_, err := NormalizeTask("WB", task)
	elapsed := time.Since(start)

	if got, want := CategoryOf(err), CategoryValidation; got != want {
		t.Fatalf("NormalizeTask() category = %q, want %q; error = %v", got, want, err)
	}
	if elapsed > time.Second {
		t.Fatalf("NormalizeTask() took %s to refuse an oversized rank, want the ceiling checked before the parse", elapsed)
	}
	if strings.Contains(err.Error(), strings.Repeat("0", 100)) {
		t.Fatalf("NormalizeTask() error quotes the rejected rank back; length = %d", len(err.Error()))
	}
}

// TestNormalizeTaskCountsDependenciesAfterDeduplication keeps the count ceiling
// describing the stored document rather than the caller's raw argument list.
func TestNormalizeTaskCountsDependenciesAfterDeduplication(t *testing.T) {
	dependencies := make([]string, 0, MaxDependencyCount*2)
	for range MaxDependencyCount * 2 {
		dependencies = append(dependencies, "WB-01K0M6B8A4FTT8C39MXXYTW7C6")
	}
	task := sizedTask(func(task *TaskData) { task.Dependencies = dependencies })

	normalized, err := NormalizeTask("WB", task)
	if err != nil {
		t.Fatalf("NormalizeTask() error = %v", err)
	}
	if len(normalized.Dependencies) != 1 {
		t.Fatalf("NormalizeTask() dependencies = %#v, want one deduplicated dependency", normalized.Dependencies)
	}
}

// TestValidateOperationRefusesAnAbsurdRankWithoutParsingIt covers the read path
// a stored operation document takes, which reaches parseRank without passing
// through NormalizeTask at all.
func TestValidateOperationRefusesAnAbsurdRankWithoutParsingIt(t *testing.T) {
	operation := Operation{
		ID:    "01K0M6B8A4FTT8C39MXXYTW7C7",
		Type:  OperationFieldSet,
		Field: "rank",
		Value: oversizedRank(4_000_002),
	}

	start := time.Now()
	err := validateFieldSetOperation(operation)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("validateFieldSetOperation() error = nil, want an oversized rank refused")
	}
	if elapsed > time.Second {
		t.Fatalf("validateFieldSetOperation() took %s to refuse an oversized rank, want the ceiling checked before the parse", elapsed)
	}
	if strings.Contains(err.Error(), strings.Repeat("0", 100)) {
		t.Fatalf("validateFieldSetOperation() error quotes the rejected rank back; length = %d", len(err.Error()))
	}
}

// TestNormalizeTaskCountsLabelsAfterDeduplication keeps the count ceiling
// describing the stored document rather than the caller's raw argument list.
func TestNormalizeTaskCountsLabelsAfterDeduplication(t *testing.T) {
	labels := make([]string, 0, MaxLabelCount*2)
	for range MaxLabelCount * 2 {
		labels = append(labels, "release")
	}
	task := sizedTask(func(task *TaskData) { task.Labels = labels })

	normalized, err := NormalizeTask("WB", task)
	if err != nil {
		t.Fatalf("NormalizeTask() error = %v", err)
	}
	if len(normalized.Labels) != 1 {
		t.Fatalf("NormalizeTask() labels = %#v, want one deduplicated label", normalized.Labels)
	}
}

// A status ceiling that shifted by one would narrow what a project has already
// stored, so the boundary is pinned on both sides.
func TestStatusCeilingsAcceptExactlyTheirLimit(t *testing.T) {
	name := Status(strings.Repeat("a", MaxStatusNameBytes))
	if err := validateStatusToken(name); err != nil {
		t.Fatalf("validateStatusToken(%d bytes) error = %v", len(name), err)
	}
	if err := validateStatusToken(name + "a"); err == nil {
		t.Fatalf("validateStatusToken(%d bytes) error = nil, want a rejection", len(name)+1)
	}

	label := strings.Repeat("L", MaxStatusLabelBytes)
	if err := validateStatusLabel(label); err != nil {
		t.Fatalf("validateStatusLabel(%d bytes) error = %v", len(label), err)
	}
	if err := validateStatusLabel(label + "L"); err == nil {
		t.Fatalf("validateStatusLabel(%d bytes) error = nil, want a rejection", len(label)+1)
	}

	if _, err := NewVocabulary(manyStatuses(MaxStatusCount), nil, nil); err != nil {
		t.Fatalf("NewVocabulary(%d statuses) error = %v", MaxStatusCount, err)
	}
	if _, err := NewVocabulary(manyStatuses(1), manyAliases(MaxStatusAliasCount), nil); err != nil {
		t.Fatalf("NewVocabulary(%d aliases) error = %v", MaxStatusAliasCount, err)
	}
	if _, err := NewVocabulary(manyStatuses(1), nil, manyRetirements(MaxStatusRetiredCount)); err != nil {
		t.Fatalf("NewVocabulary(%d retirements) error = %v", MaxStatusRetiredCount, err)
	}
}

// Every built-in status has to satisfy the token rule, or the rule would be one
// this repository's own data cannot meet.
func TestBuiltInStatusesSatisfyTheTokenRule(t *testing.T) {
	for _, definition := range DefaultVocabulary().Definitions() {
		if err := validateStatusToken(definition.Status); err != nil {
			t.Errorf("validateStatusToken(%q) error = %v", definition.Status, err)
		}
		if err := validateStatusLabel(definition.Label); err != nil {
			t.Errorf("validateStatusLabel(%q) error = %v", definition.Label, err)
		}
	}
}

// The charset is the intersection of a bare shell token, an HTML attribute
// value, a commit subject line, and a SQLite TEXT column. These are values that
// would need escaping, quoting or a case rule in at least one of them.
func TestStatusTokenRuleRejectsValuesThatWouldNeedEscaping(t *testing.T) {
	for _, status := range []Status{
		"", " ", "In Progress", "in progress", "In-Progress", "DONE",
		"-leading", "trailing-", "double--dash", "under_score", "dot.separated",
		`quote"d`, "<script>", "semi;colon", "new\nline", "tab\there", "sla/sh", "star*", "Ünicode", "emoji🙂",
	} {
		if err := validateStatusToken(status); err == nil {
			t.Errorf("validateStatusToken(%q) error = nil, want a rejection", status)
		}
	}
	for _, status := range []Status{"a", "0", "done", "in-progress", "a1-b2-c3", "awaiting-review"} {
		if err := validateStatusToken(status); err != nil {
			t.Errorf("validateStatusToken(%q) error = %v, want it accepted", status, err)
		}
	}
}

func sizedTask(apply func(*TaskData)) TaskData {
	task := TaskData{Title: "Task", Status: StatusReady, Priority: PriorityMedium, Rank: "1/1"}
	apply(&task)
	return task
}

func distinctLabels(count int) []string {
	labels := make([]string, 0, count)
	for index := range count {
		labels = append(labels, "label-"+strconv.Itoa(index))
	}
	return labels
}

// oversizedRank returns a reduced rational exactly bytes long, so the ceiling is
// the only thing that can reject it.
func oversizedRank(bytes int) string {
	return "1" + strings.Repeat("0", bytes-3) + "/7"
}

func distinctDependencies(count int) []string {
	const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	dependencies := make([]string, 0, count)
	for index := range count {
		suffix := []byte("01K0M6B8A4FTT8C39MXXYTW700")
		suffix[25] = crockford[index%32]
		suffix[24] = crockford[index/32%32]
		suffix[23] = crockford[index/1024%32]
		dependencies = append(dependencies, "WB-"+string(suffix))
	}
	return dependencies
}
