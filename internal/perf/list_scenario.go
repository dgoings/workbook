package perf

import "context"

// measureColdList measures the cold task read path. Like every other cold CLI
// scenario, the disposable projection is rebuilt by the untimed setup command
// before this measurement starts.
func measureColdList(ctx context.Context, dependencies scenarioDependencies, spec RunSpec, fixture Fixture, _ int) (Sample, error) {
	return measureColdCLICommand(ctx, dependencies, spec, fixture.Root, []string{"list", "--json"}), nil
}
