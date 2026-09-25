// spike.js — RF2 load test, phase 1 of 2: "the system must not lose a
// request during a spike" (docs/openapi.yaml, docs/messaging.md,
// .ai-agents/PLAN.md Phase 4 item 4).
//
// This script only generates the spike load and records what was accepted.
// Confirming that every accepted video later reaches a final status
// (DONE/FAILED) is done by the companion script confirm.js, run as a
// SEPARATE k6 process right after this one finishes (see the `loadtest`
// Makefile target). They are split into two processes, not two scenarios
// of one run, because:
//   - k6's `open()` (the only way to read a file from a k6 script) works
//     only in the init context, before any VU/setup/teardown code runs —
//     it cannot read a file *written during the same run*.
//   - k6 has no in-script file-write API and no built-in way to hand raw
//     per-request data (like a list of accepted video ids) from many
//     concurrent VUs back to a later scenario or to teardown().
// The reliable way to pass that data to a later, separate `k6 run` is
// therefore stdout: every accepted upload logs one `ACCEPTED_ID <id> <user
// index>` line, and every registered user logs one `USER_TOKEN <index>
// <token>` line. The Makefile target redirects this script's stdout to
// docs/loadtest/spike.log, and confirm.js reads that file back at its own
// init time (a fresh process, started only once this one has exited, so
// the log is complete on disk).
//
// What "not lost" means here, precisely:
//   (a) every POST /api/v1/videos call gets HTTP 202 (no 5xx, no transport
//       error, no dropped connection) while load ramps from a baseline up
//       to a spike and back down — checked and reported by THIS script;
//   (b) every accepted video id later reaches DONE or FAILED, never stuck
//       PENDING/PROCESSING — checked and reported by confirm.js.
//
// No k6 `thresholds` are used: they can abort a run mid-flight, and we
// want the full run to complete and report, then a pass/fail verdict
// computed from the completed summary in handleSummary().

import http from 'k6/http';
import { check } from 'k6';
import { Counter, Rate, Trend } from 'k6/metrics';

// No jslib import here on purpose: the k6 container runs on the compose
// network only (no route to jslib.k6.io in this sandbox), so the summary
// text below is built by hand instead of via the usual k6-summary jslib.

const BASE_URL = __ENV.BASE_URL || 'http://localhost:8080';
const USER_COUNT = Number(__ENV.LOADTEST_USERS || 8);
const OUT_DIR = __ENV.LOADTEST_OUT_DIR || '/out';

// The tiny fixture video: 64x48, ~1s, mpeg4 — fast for ffmpeg to decode and
// zip so the backend can actually drain the queue inside confirm.js's
// observation window. Generated once with:
//   ffmpeg -y -f lavfi -i "testsrc=size=64x48:rate=25:duration=1" \
//     -c:v mpeg4 -pix_fmt yuv420p deploy/loadtest/tiny.mp4
const tinyVideo = open('./tiny.mp4', 'b');

const uploadsAccepted = new Counter('uploads_accepted'); // 202 count
const uploadsRejected = new Counter('uploads_rejected'); // non-202 / transport error count
const uploadFailureRate = new Rate('upload_failure_rate'); // non-202-or-error / total
const uploadDuration = new Trend('upload_duration', true); // POST /videos latency, ms

export function setup() {
  const tokens = [];
  for (let i = 0; i < USER_COUNT; i++) {
    const email = `loadtest-user-${i}-${Date.now()}@example.com`;
    const password = 'loadtest-pw-correct-horse-battery';
    const registerRes = http.post(
      `${BASE_URL}/api/v1/auth/register`,
      JSON.stringify({ name: `Load Test User ${i}`, email, password }),
      { headers: { 'Content-Type': 'application/json' } }
    );
    if (registerRes.status !== 201) {
      throw new Error(
        `setup: register user ${i} failed: ${registerRes.status} ${registerRes.body}`
      );
    }
    const loginRes = http.post(
      `${BASE_URL}/api/v1/auth/login`,
      JSON.stringify({ email, password }),
      { headers: { 'Content-Type': 'application/json' } }
    );
    if (loginRes.status !== 200) {
      throw new Error(`setup: login user ${i} failed: ${loginRes.status} ${loginRes.body}`);
    }
    const token = loginRes.json('access_token');
    tokens.push(token);
    // Picked up by confirm.js so it can poll each video with the same
    // user's token (ownership is enforced: GET /videos/{id} 404s for
    // another user's video).
    console.log(`USER_TOKEN ${i} ${token}`);
  }
  return { tokens };
}

export default function (data) {
  const { tokens } = data;
  const userIndex = __VU % tokens.length;
  const token = tokens[userIndex];
  const fileName = `spike-${__VU}-${__ITER}-${Date.now()}.mp4`;
  const payload = { videos: http.file(tinyVideo, fileName, 'video/mp4') };

  const res = http.post(`${BASE_URL}/api/v1/videos`, payload, {
    headers: { Authorization: `Bearer ${token}` },
    tags: { name: 'POST /api/v1/videos' },
  });
  uploadDuration.add(res.timings.duration);

  const ok = check(res, { 'upload returns 202': (r) => r.status === 202 });
  uploadFailureRate.add(!ok);

  if (ok) {
    uploadsAccepted.add(1);
    let id = null;
    try {
      id = res.json('videos')[0].id;
    } catch (e) {
      id = null;
    }
    if (id) {
      console.log(`ACCEPTED_ID ${id} ${userIndex}`);
    } else {
      console.log(`ACCEPTED_ID_PARSE_ERROR status=${res.status} body=${res.body}`);
    }
  } else {
    uploadsRejected.add(1);
    console.log(`REJECTED status=${res.status} body=${(res.body || '').slice(0, 200)}`);
  }
}

// options: spike load profile. ramping-arrival-rate is used (not plain VUs)
// because it states the actual request rate directly, which is what "no
// lost request during a spike in traffic" is about.
//
// Stage breakdown (arrival rate, iterations/s):
//   0s-10s  : 5 rps   (baseline)
//   10s-25s : 5 -> 30 rps, ramping (spike)
//   25s-35s : 30 -> 5 rps, back to baseline
//   35s-40s : 5 -> 0 rps, ramp down
// Total wall time: 40s. Expected accepted uploads: roughly 350-450,
// depending on exact ramp shape.
export const options = {
  scenarios: {
    spike: {
      executor: 'ramping-arrival-rate',
      startRate: 5,
      timeUnit: '1s',
      preAllocatedVUs: 50,
      maxVUs: 120,
      stages: [
        { target: 5, duration: '10s' },
        { target: 30, duration: '15s' },
        { target: 5, duration: '10s' },
        { target: 0, duration: '5s' },
      ],
    },
  },
  summaryTrendStats: ['avg', 'min', 'med', 'p(90)', 'p(95)', 'p(99)', 'max'],
};

export function handleSummary(data) {
  const m = data.metrics;
  const get = (name, stat) => (m[name] && m[name].values ? m[name].values[stat] : undefined);

  const accepted = get('uploads_accepted', 'count') || 0;
  const rejected = get('uploads_rejected', 'count') || 0;
  const httpReqs = get('http_reqs', 'count') || 0;
  const httpReqFailed = get('http_req_failed', 'rate');

  const stats = {
    avg: get('upload_duration', 'avg'),
    p50: get('upload_duration', 'med'),
    p90: get('upload_duration', 'p(90)'),
    p95: get('upload_duration', 'p(95)'),
    p99: get('upload_duration', 'p(99)'),
    max: get('upload_duration', 'max'),
  };

  const fmt = (v) => (v === undefined ? 'n/a' : v.toFixed(2));

  const md = `# k6 spike load test — phase 1 (upload spike)

Generated: ${new Date().toISOString()}

- Uploads accepted (202): ${accepted}
- Uploads rejected (non-202 / transport error): ${rejected}
- Total HTTP requests: ${httpReqs}
- http_req_failed rate (k6 built-in): ${
    httpReqFailed !== undefined ? (httpReqFailed * 100).toFixed(3) + '%' : 'n/a'
  }

## POST /api/v1/videos latency (ms)

| stat | ms |
|---|---|
| avg | ${fmt(stats.avg)} |
| p50 | ${fmt(stats.p50)} |
| p90 | ${fmt(stats.p90)} |
| p95 | ${fmt(stats.p95)} |
| p99 | ${fmt(stats.p99)} |
| max | ${fmt(stats.max)} |

Phase 1 verdict (HTTP layer only — does not yet confirm processing
completed; see confirm.js / confirm-summary.json for that):
**${rejected === 0 && accepted > 0 ? 'PASS' : 'FAIL'}** — ${
    rejected === 0 ? 'zero non-202 responses' : rejected + ' non-202/error response(s)'
  }.
`;

  const stdout = `SPIKE PHASE SUMMARY
accepted=${accepted} rejected=${rejected} http_reqs=${httpReqs} http_req_failed_rate=${
    httpReqFailed !== undefined ? (httpReqFailed * 100).toFixed(3) + '%' : 'n/a'
  }
upload_duration_ms avg=${fmt(stats.avg)} p50=${fmt(stats.p50)} p90=${fmt(stats.p90)} p95=${fmt(
    stats.p95
  )} p99=${fmt(stats.p99)} max=${fmt(stats.max)}
`;

  return {
    stdout,
    [`${OUT_DIR}/spike-summary.json`]: JSON.stringify(data, null, 2),
    [`${OUT_DIR}/spike-report.md`]: md,
  };
}
