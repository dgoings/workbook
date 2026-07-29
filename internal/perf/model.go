package perf

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sort"
	"time"
)

const (
	ReportFormat  = "workbook.performance-report"
	ReportVersion = 2
)

type FixtureSpec struct {
	TotalTasks        int    `json:"totalTasks"`
	ActiveTasks       int    `json:"activeTasks"`
	TombstonedTasks   int    `json:"tombstonedTasks"`
	OperationsPerTask int    `json:"operationsPerTask"`
	ObjectFormat      string `json:"objectFormat"`
}

type Targets struct {
	WarmP95Milliseconds float64 `json:"warmP95Milliseconds"`
	ColdP95Milliseconds float64 `json:"coldP95Milliseconds"`
	BurstMilliseconds   float64 `json:"burstMilliseconds"`
}

type DurationStatistic string

const (
	DurationP95         DurationStatistic = "p95"
	DurationEverySample DurationStatistic = "every-sample"
)

type DurationComparison string

const (
	DurationAtMost   DurationComparison = "at-most"
	DurationLessThan DurationComparison = "less-than"
)

// ScenarioTarget sets the duration policy and optional exclusive Git process
// limit for one measured scenario. A zero MaxGitProcesses has no process
// target.
type ScenarioTarget struct {
	DurationStatistic  DurationStatistic  `json:"durationStatistic"`
	DurationComparison DurationComparison `json:"durationComparison"`
	MaxMilliseconds    float64            `json:"maxMilliseconds"`
	MaxGitProcesses    int                `json:"maxGitProcesses,omitempty"`
}

type Sample struct {
	Duration     time.Duration `json:"-"`
	Milliseconds float64       `json:"milliseconds"`
	GitProcesses int           `json:"gitProcesses"`
	ExitCode     int           `json:"exitCode"`
	TimedOut     bool          `json:"timedOut"`
	Error        string        `json:"error,omitempty"`
}

type Summary struct {
	Completed          int     `json:"completed"`
	TimedOut           int     `json:"timedOut"`
	MinMilliseconds    float64 `json:"minMilliseconds"`
	MedianMilliseconds float64 `json:"medianMilliseconds"`
	P95Milliseconds    float64 `json:"p95Milliseconds"`
	P95GitProcesses    int     `json:"p95GitProcesses"`
}

type ScenarioResult struct {
	Name    string          `json:"name"`
	Surface string          `json:"surface"`
	Target  *ScenarioTarget `json:"target,omitempty"`
	Outcome string          `json:"outcome,omitempty"`
	Samples []Sample        `json:"samples"`
	Summary Summary         `json:"summary"`
}

type RepositoryMetrics struct {
	LooseRefEnumerationMilliseconds  float64 `json:"looseRefEnumerationMilliseconds"`
	PackedRefEnumerationMilliseconds float64 `json:"packedRefEnumerationMilliseconds"`
	LooseObjects                     int64   `json:"looseObjects"`
	LooseObjectBytes                 int64   `json:"looseObjectBytes"`
	PackedObjects                    int64   `json:"packedObjects"`
	PackBytes                        int64   `json:"packBytes"`
}

type Environment struct {
	OS              string `json:"os"`
	Arch            string `json:"arch"`
	GitVersion      string `json:"gitVersion"`
	GoVersion       string `json:"goVersion"`
	WorkbookVersion string `json:"workbookVersion"`
	WorkbookCommit  string `json:"workbookCommit"`
}

type Report struct {
	Format      string            `json:"format"`
	Version     int               `json:"version"`
	Phase       string            `json:"phase"`
	GeneratedAt time.Time         `json:"generatedAt"`
	Environment Environment       `json:"environment"`
	Fixture     FixtureSpec       `json:"fixture"`
	Targets     Targets           `json:"targets"`
	Scenarios   []ScenarioResult  `json:"scenarios"`
	Repository  RepositoryMetrics `json:"repository"`
}

func Summarize(samples []Sample) Summary {
	var summary Summary
	var milliseconds []float64
	var gitProcesses []int
	for _, sample := range samples {
		gitProcesses = append(gitProcesses, sample.GitProcesses)
		if sample.TimedOut {
			summary.TimedOut++
			continue
		}
		if sample.ExitCode != 0 || sample.Error != "" {
			continue
		}
		summary.Completed++
		milliseconds = append(milliseconds, durationMilliseconds(sample))
	}
	sort.Ints(gitProcesses)
	if len(gitProcesses) != 0 {
		summary.P95GitProcesses = gitProcesses[nearestRankIndex(len(gitProcesses), 0.95)]
	}
	if len(milliseconds) == 0 {
		return summary
	}

	sort.Float64s(milliseconds)
	summary.MinMilliseconds = milliseconds[0]
	summary.MedianMilliseconds = median(milliseconds)
	p95Index := nearestRankIndex(len(milliseconds), 0.95)
	summary.P95Milliseconds = milliseconds[p95Index]
	return summary
}

func (r Report) WriteJSON(w io.Writer) error {
	return json.NewEncoder(w).Encode(r.normalized())
}

func (r Report) WriteMarkdown(w io.Writer) error {
	r = r.normalized()
	if _, err := fmt.Fprintln(w, "# Workbook performance report"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "\nPhase: %s\n\n", r.Phase); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, "## Reference budgets"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "Baseline targets are reference budgets, not achieved guarantees: warm p95 %.2f ms, cold p95 %.2f ms, burst %.2f ms.\n", r.Targets.WarmP95Milliseconds, r.Targets.ColdP95Milliseconds, r.Targets.BurstMilliseconds); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, "\n## Scenarios"); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, "| Scenario | Surface | Completed | Timed out | Min (ms) | Median (ms) | P95 (ms) | P95 Git processes | Target duration | Target Git processes | Outcome |"); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, "| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- |"); err != nil {
		return err
	}
	for _, scenario := range r.Scenarios {
		targetDuration, targetGitProcesses := "-", "-"
		if scenario.Target != nil {
			targetDuration = formatDurationTarget(*scenario.Target)
			if scenario.Target.MaxGitProcesses > 0 {
				targetGitProcesses = fmt.Sprintf("< %d", scenario.Target.MaxGitProcesses)
			}
		}
		if _, err := fmt.Fprintf(w, "| %s | %s | %d | %d | %.2f | %.2f | %.2f | %d | %s | %s | %s |\n", scenario.Name, scenario.Surface, scenario.Summary.Completed, scenario.Summary.TimedOut, scenario.Summary.MinMilliseconds, scenario.Summary.MedianMilliseconds, scenario.Summary.P95Milliseconds, scenario.Summary.P95GitProcesses, targetDuration, targetGitProcesses, scenario.Outcome); err != nil {
			return err
		}
	}
	return nil
}

func (r Report) normalized() Report {
	r.Scenarios = append([]ScenarioResult(nil), r.Scenarios...)
	for i := range r.Scenarios {
		scenario := &r.Scenarios[i]
		scenario.Samples = append([]Sample(nil), scenario.Samples...)
		for j := range scenario.Samples {
			scenario.Samples[j].Milliseconds = durationMilliseconds(scenario.Samples[j])
		}
		scenario.Summary = Summarize(scenario.Samples)
		scenario.Outcome = scenarioOutcome(*scenario)
	}
	sort.SliceStable(r.Scenarios, func(i, j int) bool {
		return r.Scenarios[i].Name < r.Scenarios[j].Name
	})
	return r
}

func scenarioOutcome(scenario ScenarioResult) string {
	if scenario.Target == nil {
		return "not-evaluated"
	}
	if len(scenario.Samples) == 0 {
		return "failed"
	}
	failed := false
	for _, sample := range scenario.Samples {
		if sample.TimedOut {
			return "timeout"
		}
		if sample.ExitCode != 0 || sample.Error != "" {
			failed = true
			continue
		}
	}
	if failed {
		return "failed"
	}
	if scenarioTargetMissed(scenario) {
		return "miss"
	}
	return "pass"
}

func scenarioTargetMissed(scenario ScenarioResult) bool {
	target := *scenario.Target
	if target.MaxGitProcesses > 0 {
		for _, sample := range scenario.Samples {
			if sample.GitProcesses >= target.MaxGitProcesses {
				return true
			}
		}
	}

	switch target.DurationStatistic {
	case DurationP95:
		return durationExceeds(Summarize(scenario.Samples).P95Milliseconds, target)
	case DurationEverySample:
		for _, sample := range scenario.Samples {
			if sample.TimedOut || sample.ExitCode != 0 || sample.Error != "" {
				continue
			}
			if durationExceeds(durationMilliseconds(sample), target) {
				return true
			}
		}
	}
	return false
}

func durationExceeds(milliseconds float64, target ScenarioTarget) bool {
	switch target.DurationComparison {
	case DurationAtMost:
		return milliseconds > target.MaxMilliseconds
	case DurationLessThan:
		return milliseconds >= target.MaxMilliseconds
	default:
		return false
	}
}

func formatDurationTarget(target ScenarioTarget) string {
	statistic := "each"
	if target.DurationStatistic == DurationP95 {
		statistic = "p95"
	}
	comparison := "<="
	if target.DurationComparison == DurationLessThan {
		comparison = "<"
	}
	return fmt.Sprintf("%s %s %.2f ms", statistic, comparison, target.MaxMilliseconds)
}

func durationMilliseconds(sample Sample) float64 {
	return float64(sample.Duration) / float64(time.Millisecond)
}

func nearestRankIndex(length int, percentile float64) int {
	return int(math.Ceil(percentile*float64(length))) - 1
}

func median(values []float64) float64 {
	middle := len(values) / 2
	if len(values)%2 == 1 {
		return values[middle]
	}
	return (values[middle-1] + values[middle]) / 2
}
