# Quality gates (Phase 5)

Two CI-enforced checks beyond `golangci-lint` (already wired, see
`.golangci.yml` and the `golangci-lint` step in
[`.github/workflows/ci.yml`](../.github/workflows/ci.yml)):

## Coverage

Target: **>= 80% combined statement coverage** on `internal/domain` and
`internal/app` — the domain entities and use cases named in the plan
(`.ai-agents/PLAN.md`, Phase 5). Other packages (adapters, `cmd/*`) are
exercised mainly through `tests/integration/` and are not counted toward
this target.

- `make coverage` runs `go test -race -coverprofile=coverage.out
  -covermode=atomic ./internal/domain/... ./internal/app/...`, prints
  `go tool cover -func`, and exits non-zero if the combined total is below
  the threshold (`COVERAGE_THRESHOLD`, default 80).
- CI runs the same check as its own step (after "Integration tests", since
  it only needs unit tests, not the compose stack), uploads `coverage.out`
  as a build artifact, and writes the `go tool cover -func` output to the
  job summary.
- `make check` runs `coverage` too, so a coverage regression fails locally
  before it fails CI.

To inspect coverage by function or line locally:

```sh
make coverage
go tool cover -html=coverage.out   # opens an HTML report in a browser
```

## Vulnerability scanning

`make vulncheck` runs [`govulncheck`](https://golang.org/x/vuln/cmd/govulncheck)
against `./...`, using the same pinned-version/`GOTOOLCHAIN` pattern as
golangci-lint (`make tools` installs both). It is a CI step and part of
`make check`. A real finding is not suppressed — see the PR that introduced
this check for the state of the dependency tree at the time.

SonarCloud is out of scope: it needs an external account/token that this
repo does not have configured.
