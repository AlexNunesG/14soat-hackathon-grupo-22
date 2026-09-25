# k6 spike load test — phase 1 (upload spike)

Generated: 2026-09-25T03:14:48.493Z

- Uploads accepted (202): 499
- Uploads rejected (non-202 / transport error): 0
- Total HTTP requests: 515
- http_req_failed rate (k6 built-in): 0.000%

## POST /api/v1/videos latency (ms)

| stat | ms |
|---|---|
| avg | 7.08 |
| p50 | 6.52 |
| p90 | 7.59 |
| p95 | 8.24 |
| p99 | 10.65 |
| max | 230.96 |

Phase 1 verdict (HTTP layer only — does not yet confirm processing
completed; see confirm.js / confirm-summary.json for that):
**PASS** — zero non-202 responses.
