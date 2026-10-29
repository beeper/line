#!/usr/bin/env bash
set -euo pipefail
root=$(git rev-parse --show-toplevel)
cd "$root"
mode=${1:-normal}; if (($#)); then shift; fi
parallel=${TEST_PACKAGE_PARALLELISM:-2}; timeout=${TEST_TIMEOUT:-10m}
out=${TEST_OUTPUT_DIR:-"$root/.opencode-work/test-optimization/validation"}
mkdir -p "$out/tmp"; export TMPDIR="$out/tmp" GOTMPDIR="$out/tmp"
export GOMAXPROCS=${GOMAXPROCS:-4}
json=(); [[ ${TEST_JSON:-0} == 1 ]] && json=(-json)
format_check() {
  local files; files=$(go list -f '{{range .GoFiles}}{{$.Dir}}/{{.}}{{"\n"}}{{end}}{{range .TestGoFiles}}{{$.Dir}}/{{.}}{{"\n"}}{{end}}{{range .XTestGoFiles}}{{$.Dir}}/{{.}}{{"\n"}}{{end}}' "$@")
  [[ -z $(gofmt -l $files) ]] || { gofmt -l $files; return 1; }
}
lint() {
  local packages=() package
  while IFS= read -r package; do [[ $package == */ltsm ]] || packages+=("$package"); done < <(go list "$@")
  ((${#packages[@]})) && { go vet "${packages[@]}"; staticcheck "${packages[@]}"; }
}
normal() { go test -p "$parallel" -count=1 -timeout="$timeout" "${json[@]}" ./...; }
race() { go test -race -p "$parallel" -count=1 -timeout="$timeout" "${json[@]}" ./...; }
coverage() { go test -p "$parallel" -count=1 -timeout="$timeout" -covermode=atomic -coverprofile="$out/coverage.out" "${json[@]}" ./...; python3 -B scripts/check_test_coverage.py "$out/coverage.out"; }
case $mode in
 fast) (($#)) || { echo 'usage: scripts/test.sh fast ./pkg'; exit 2; }; format_check "$@"; lint "$@"; run=(); [[ -z ${TEST_RUN:-} ]] || run=(-run "$TEST_RUN"); go test -p "$parallel" -count=1 -timeout="$timeout" "${json[@]}" "${run[@]}" "$@" ;;
 normal) normal ;;
 race) race ;;
 coverage) coverage ;;
 release)
   format_check ./...; lint ./...; go mod verify; python3 -B -m unittest discover -s scripts -p test_check_test_coverage.py
   normal; race; coverage; go build ./...
   source_epoch=${SOURCE_DATE_EPOCH:-$(git show -s --format=%ct HEAD)}
   SOURCE_DATE_EPOCH=$source_epoch ./build.sh -o "$out/matrix-line"
   "$out/matrix-line" --version
   ;;
 *) echo 'Modes: fast, normal, race, coverage, release'; exit 2 ;;
esac
