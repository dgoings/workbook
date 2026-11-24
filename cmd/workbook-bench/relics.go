package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/dgoings/workbook/internal/perf"
)

const (
	relicScaleFull  = "full"
	relicScaleSmoke = "smoke"
)

// relicRefusedFlags are the flags whose meaning the relic scale owns.
var relicRefusedFlags = []string{"scaling", "storage-resources", "scenario", "scaling-point", "tasks", "tombstones", "operations"}

// configureRelicOptions checks --relics and --relic-scale. It reports
// whether the run is a relic run, which then needs no other mode's checks.
func configureRelicOptions(flags *flag.FlagSet, options *options) (bool, error) {
	explicit := explicitFlagNames(flags)
	if !options.relics {
		if _, set := explicit["relic-scale"]; set {
			return false, fmt.Errorf("--relic-scale requires --relics")
		}
		return false, nil
	}
	for _, name := range relicRefusedFlags {
		if _, set := explicit[name]; set {
			return true, fmt.Errorf("--%s is not valid with --relics; the relic scale defines its own fixture", name)
		}
	}
	if _, err := relicFixtureSpec(options.relicScale); err != nil {
		return true, err
	}
	return true, resolveReportPaths(options)
}

func relicFixtureSpec(scale string) (perf.RelicFixtureSpec, error) {
	switch scale {
	case relicScaleFull:
		return perf.FullRelicFixtureSpec(), nil
	case relicScaleSmoke:
		return perf.SmokeRelicFixtureSpec(), nil
	default:
		return perf.RelicFixtureSpec{}, fmt.Errorf("--relic-scale must be %s or %s, got %q", relicScaleFull, relicScaleSmoke, scale)
	}
}

// runRelicBenchmark builds the relic set at the chosen scale and measures the
// relic family; the report carries its scenarios and its relics section.
func runRelicBenchmark(ctx context.Context, options options, environment perf.Environment) (perf.Report, error) {
	relics, err := relicFixtureSpec(options.relicScale)
	if err != nil {
		return perf.Report{}, err
	}
	root, err := os.MkdirTemp("", "workbook-relics-")
	if err != nil {
		return perf.Report{}, fmt.Errorf("create temporary relic fixture root: %w", err)
	}
	defer os.RemoveAll(root)

	fixtureSpec := relics.TaskFixture(options.objectFormat)
	scenarios, report, err := perf.RunRelicBenchmark(ctx, perf.RelicRunSpec{
		RunSpec: benchmarkRunSpec(options, fixtureSpec),
		Relics:  relics,
	}, filepath.Join(root, "relics"))
	if err != nil {
		return perf.Report{}, fmt.Errorf("run relic scenarios: %w", err)
	}
	return perf.Report{
		Format:      perf.ReportFormat,
		Version:     perf.ReportVersion,
		GeneratedAt: time.Now().UTC(),
		Environment: environment,
		Fixture:     fixtureSpec,
		Scenarios:   scenarios,
		Relics:      report,
	}, nil
}
