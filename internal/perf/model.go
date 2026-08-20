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
	ReportVersion = 3
)

type FixtureSpec struct {
	TotalTasks        int    `json:"totalTasks"`
	ActiveTasks       int    `json:"activeTasks"`
	TombstonedTasks   int    `json:"tombstonedTasks"`
	OperationsPerTask int    `json:"operationsPerTask"`
	ObjectFormat      string `json:"objectFormat"`
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
	Name    string   `json:"name"`
	Surface string   `json:"surface"`
	Outcome string   `json:"outcome,omitempty"`
	Samples []Sample `json:"samples"`
	Summary Summary  `json:"summary"`
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
	OS                   string `json:"os"`
	Arch                 string `json:"arch"`
	GitVersion           string `json:"gitVersion"`
	GoVersion            string `json:"goVersion"`
	WorkbookVersion      string `json:"workbookVersion"`
	WorkbookCommit       string `json:"workbookCommit"`
	WorkbookBinarySHA256 string `json:"workbookBinarySha256"`
}

type Report struct {
	Format      string            `json:"format"`
	Version     int               `json:"version"`
	GeneratedAt time.Time         `json:"generatedAt"`
	Environment Environment       `json:"environment"`
	Fixture     FixtureSpec       `json:"fixture"`
	Scenarios   []ScenarioResult  `json:"scenarios"`
	Repository  RepositoryMetrics `json:"repository"`
	// ProjectionRefresh is present only when the projection refresh
	// change-count family was measured.
	ProjectionRefresh *ProjectionRefreshReport `json:"projectionRefresh,omitempty"`
	// StorageResources is the descriptive storage and peak-resource
	// accounting. It is absent from runs that did not measure it.
	StorageResources *StorageResourceReport `json:"storageResources,omitempty"`
	// WatcherSteadyState is the descriptive account of a running
	// `workbook sync --watch`. It is absent from runs that did not observe one.
	WatcherSteadyState *WatcherSteadyStateReport `json:"watcherSteadyState,omitempty"`
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
	if _, err := fmt.Fprintln(w, "\n## Scenarios"); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, "| Scenario | Surface | Completed | Timed out | Min (ms) | Median (ms) | P95 (ms) | P95 Git processes | Outcome |"); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, "| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | --- |"); err != nil {
		return err
	}
	for _, scenario := range r.Scenarios {
		if _, err := fmt.Fprintf(w, "| %s | %s | %d | %d | %.2f | %.2f | %.2f | %d | %s |\n", scenario.Name, scenario.Surface, scenario.Summary.Completed, scenario.Summary.TimedOut, scenario.Summary.MinMilliseconds, scenario.Summary.MedianMilliseconds, scenario.Summary.P95Milliseconds, scenario.Summary.P95GitProcesses, scenario.Outcome); err != nil {
			return err
		}
	}
	if r.ProjectionRefresh != nil {
		if err := r.ProjectionRefresh.writeMarkdown(w); err != nil {
			return err
		}
	}
	if err := writeStorageResourceMarkdown(w, r.StorageResources); err != nil {
		return err
	}
	return writeWatcherMarkdown(w, r.WatcherSteadyState)
}

func (r Report) normalized() Report {
	r.Scenarios = append(make([]ScenarioResult, 0, len(r.Scenarios)), r.Scenarios...)
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

// scenarioOutcome reports harness health, not a performance judgment: a
// report whose samples timed out or failed is not a valid comparison
// baseline, while a completed scenario is simply measured.
func scenarioOutcome(scenario ScenarioResult) string {
	failed := len(scenario.Samples) == 0
	for _, sample := range scenario.Samples {
		if sample.TimedOut {
			return "timeout"
		}
		if sample.ExitCode != 0 || sample.Error != "" {
			failed = true
		}
	}
	if failed {
		return "failed"
	}
	return "completed"
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
