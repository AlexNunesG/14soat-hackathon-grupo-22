# Integration tests

Black-box tests for the video processor. They only talk to the app over HTTP.
No mocks: test videos are generated and processed by the real `ffmpeg`, and the
tests check the real ZIP and PNG output. Each endpoint has its own file.

## Status: every test is skipped

The original implementation (`main.go`) was removed so the app can be rebuilt.
Every test starts with `notImplemented(t)`, which skips it, so the suite stays
green while there is nothing to test.

To enable a test, delete its `notImplemented(t)` line in the same pull request
that implements the behavior it checks. An enabled test fails with "no app to
test" until there is Go code at the module root to build (or `BASE_URL` is set).

To see what is still pending:

```sh
grep -rn 'notImplemented(t)$' tests/integration/
```

| File | Covers |
|---|---|
| `index_test.go` | `GET /` |
| `upload_test.go` | `POST /upload` |
| `download_test.go` | `GET /download/:filename` |
| `status_test.go` | `GET /api/status` |
| `static_test.go` | `GET /uploads/*`, `GET /outputs/*` |
| `cors_test.go` | CORS headers and `OPTIONS` preflight on every route |
| `startup_test.go` | Process startup (`PORT`, default 8080, bind failure) and graceful shutdown |
| `e2e_test.go` | Upload → status → download |
| `main_test.go`, `helpers_test.go` | Harness and shared helpers |

## Requirements

`ffmpeg` in `PATH`. It generates the test videos, and the reference app uses it too.

## Running against the reference app (default)

```sh
go test -v -count=1 ./tests/integration/
```

The harness builds the app from the module root, runs it as a separate process
on a free port (`PORT` env var) in a temporary working directory, and stops it
with `SIGTERM` at the end. All enabled tests run. If the module root has no Go
code, nothing is built or started.

Coverage of the app (a relative `COVERAGE_OUT` is written at the module root):

```sh
COVERAGE_OUT=coverage.out go test -count=1 ./tests/integration/
go tool cover -func=coverage.out
```

CI (`.github/workflows/ci.yml`) runs this suite with `-race` on every pull
request and on pushes to `main`, after `gofmt` and `go vet`. When an app was
built, the coverage summary is shown on the run page and uploaded as an
artifact.

## Running against another implementation

```sh
BASE_URL=http://localhost:8080 go test -v -count=1 ./tests/integration/
```

Nothing is built or started. Tests marked with `requireReferenceApp` are
skipped. They reach failure paths by manipulating the reference app's
`uploads/`, `outputs/` and `temp/` directories, or they start extra instances of
its binary. Everything else is the HTTP contract a new implementation must meet.
Those tests don't assume the server starts empty.

## Contract notes

These behaviors of the original app are asserted as-is. Decide whether the new
implementation should keep them before changing the tests:

- A processing failure (ffmpeg error, no frames, ZIP error) returns HTTP 200
  with `success: false`. Only request and validation errors return 4xx/5xx.
- The "unsupported format" message lists 4 formats, but 7 are accepted
  (`mp4, avi, mov, mkv, wmv, flv, webm`, in any case).
- ZIP names have one-second resolution (`frames_YYYYMMDD_HHMMSS.zip`), so two
  uploads in the same second overwrite each other.
- `/uploads/*` publicly serves uploads that failed processing.
