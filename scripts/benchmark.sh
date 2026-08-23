#!/bin/sh
# Build the product and the benchmark harness from this working tree, run the
# benchmark scenarios, and write a dated report pair under bench-reports/.
# Every workbook-bench flag can be overridden by passing it through, e.g.:
#
#   scripts/benchmark.sh                            # full run with defaults
#   scripts/benchmark.sh --samples 5
#   scripts/benchmark.sh --scenario cli-list --tasks 50 --operations 5
#   scripts/benchmark.sh --object-format sha256
#   scripts/benchmark.sh --scaling
#
# The reports carry no pass/fail thresholds; compare a run's Markdown report
# with an earlier one to see whether the baseline has shifted.
set -eu

if ! command -v go >/dev/null 2>&1; then
	echo "workbook benchmark: go is required but was not found in PATH" >&2
	exit 1
fi
if ! command -v git >/dev/null 2>&1; then
	echo "workbook benchmark: git is required but was not found in PATH" >&2
	exit 1
fi

case $0 in
	*/*) script_directory=${0%/*} ;;
	*) script_directory=. ;;
esac
repository_root=$(CDPATH='' cd -- "${script_directory}/.." && pwd)

# Stamp the measured binary so the report names the commit it measured; a
# report that cannot be traced to a commit cannot be compared with a later one.
version=$(git -C "${repository_root}" describe --tags --always --dirty 2>/dev/null || echo dev)
commit=$(git -C "${repository_root}" rev-parse HEAD 2>/dev/null || echo unknown)
short_commit=$(git -C "${repository_root}" rev-parse --short HEAD 2>/dev/null || echo unknown)

build_directory=$(mktemp -d "${TMPDIR:-/tmp}/workbook-benchmark.XXXXXX")
trap 'rm -rf "${build_directory}"' EXIT INT TERM

(
	cd -- "${repository_root}"
	go build -trimpath \
		-ldflags "-X main.version=${version} -X main.commit=${commit}" \
		-o "${build_directory}/workbook" ./cmd/workbook
	go build -trimpath -o "${build_directory}/workbook-bench" ./cmd/workbook-bench
)

report_directory=${repository_root}/bench-reports
mkdir -p -- "${report_directory}"
timestamp=$(date -u +%Y%m%dT%H%M%SZ)
report_base=${report_directory}/${timestamp}-${short_commit}

"${build_directory}/workbook-bench" \
	--workbook "${build_directory}/workbook" \
	--samples 3 \
	--output-json "${report_base}.json" \
	--output-markdown "${report_base}.md" \
	"$@"

echo "${report_base}.md"
