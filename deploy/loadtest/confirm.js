// confirm.js — RF2 load test, phase 2 of 2: proves that every video
// accepted during the spike (spike.js) later reaches a final status
// (DONE or FAILED) — the actual "no lost request" proof. A request the API
// accepted with 202 but whose video never finishes processing IS a lost
// request in this system's terms, even though the HTTP layer accepted it.
//
// Run as a separate `k6 run` process, after spike.js has fully exited (see
// the `loadtest` Makefile target). It reads spike.js's captured stdout log
// (docs/loadtest/spike.log, mounted at $LOADTEST_OUT_DIR/spike.log) at init
// time — the only context in which k6's `open()` works — to recover:
//   - every accepted video id and which test user uploaded it
//     (`ACCEPTED_ID <id> <user index>` lines), and
//   - that user's bearer token (`USER_TOKEN <index> <token>` lines),
//     because GET /api/v1/videos/{id} enforces ownership (a video not
//     owned by the caller 404s), so polling needs the right token per id.
//
// It then runs as a single VU, single iteration (k6 `shared-iterations`,
// vus:1, iterations:1), polling all pending ids in batched rounds with a
// sleep between rounds — the same shape the task description asks for from
// a teardown-style stage, done here as the whole script's one and only
// iteration since there is no spike scenario in this process to chain
// after.

import http from 'k6/http';
import { sleep } from 'k6';
import { Counter } from 'k6/metrics';

// No jslib import here on purpose: see spike.js for why (no route to
// jslib.k6.io from the compose network in this sandbox).

const BASE_URL = __ENV.BASE_URL || 'http://localhost:8080';
const OUT_DIR = __ENV.LOADTEST_OUT_DIR || '/out';
const SPIKE_LOG = __ENV.LOADTEST_SPIKE_LOG || `${OUT_DIR}/spike.log`;
const CONFIRM_TIMEOUT_S = Number(__ENV.LOADTEST_CONFIRM_TIMEOUT_S || 180);
const CONFIRM_POLL_INTERVAL_S = Number(__ENV.LOADTEST_CONFIRM_POLL_INTERVAL_S || 3);

// --- init context: parse spike.js's captured log -----------------------

const logText = open(SPIKE_LOG);

const tokensByIndex = {};
const jobs = []; // { id, token }
{
  const seenIds = new Set();
  for (const line of logText.split('\n')) {
    // Lines come from `k6 run ... | tee spike.log`, so each carries k6's
    // own `time="..." level=info msg="..." source=console` wrapper around
    // the actual console.log text — match anywhere in the line, not only
    // at its start.
    let m = line.match(/USER_TOKEN (\d+) ([^\s"]+)/);
    if (m) {
      tokensByIndex[m[1]] = m[2];
      continue;
    }
    m = line.match(/ACCEPTED_ID ([0-9a-fA-F-]{36}) (\d+)/);
    if (m && !seenIds.has(m[1])) {
      seenIds.add(m[1]);
      jobs.push({ id: m[1], userIndex: m[2] });
    }
  }
}

const confirmedDone = new Counter('confirmed_done');
const confirmedFailedStatus = new Counter('confirmed_failed_status');
const confirmedLost = new Counter('confirmed_lost');

export const options = {
  scenarios: {
    confirm: {
      executor: 'shared-iterations',
      vus: 1,
      iterations: 1,
      maxDuration: `${CONFIRM_TIMEOUT_S + 60}s`,
    },
  },
};

export default function () {
  console.log(
    `confirm: parsed ${jobs.length} accepted id(s) and ${
      Object.keys(tokensByIndex).length
    } user token(s) from ${SPIKE_LOG}`
  );
  if (jobs.length === 0) {
    console.log('confirm: nothing to confirm (no accepted ids found in spike.log)');
    return;
  }

  const pending = new Map(jobs.map((j) => [j.id, j.userIndex]));
  const deadline = Date.now() + CONFIRM_TIMEOUT_S * 1000;
  let round = 0;

  while (pending.size > 0 && Date.now() < deadline) {
    round++;
    let checkedThisRound = 0;
    for (const [id, userIndex] of Array.from(pending.entries())) {
      const token = tokensByIndex[userIndex];
      if (!token) continue; // shouldn't happen; keep pending for a retry
      const res = http.get(`${BASE_URL}/api/v1/videos/${id}`, {
        headers: { Authorization: `Bearer ${token}` },
        tags: { name: 'GET /api/v1/videos/{id}' },
      });
      checkedThisRound++;
      if (res.status !== 200) continue; // transient; retry next round
      const status = res.json('status');
      if (status === 'DONE' || status === 'FAILED') {
        pending.delete(id);
        confirmedDone.add(1);
        if (status === 'FAILED') confirmedFailedStatus.add(1);
      }
    }
    console.log(
      `confirm: round ${round}: checked ${checkedThisRound}, ${pending.size} still pending`
    );
    if (pending.size > 0) sleep(CONFIRM_POLL_INTERVAL_S);
  }

  for (const id of pending.keys()) {
    confirmedLost.add(1);
    console.log(`LOST_OR_STUCK_ID ${id}`);
  }
  console.log(
    `confirm: final: ${jobs.length - pending.size}/${jobs.length} reached DONE/FAILED, ${
      pending.size
    } never reached a final status within ${CONFIRM_TIMEOUT_S}s`
  );
}

export function handleSummary(data) {
  const m = data.metrics;
  const get = (name, stat) => (m[name] && m[name].values ? m[name].values[stat] : undefined);

  const done = get('confirmed_done', 'count') || 0;
  const failedStatus = get('confirmed_failed_status', 'count') || 0;
  const lost = get('confirmed_lost', 'count') || 0;
  const total = jobs.length;

  const md = `# k6 spike load test — phase 2 (confirm processing completed)

Generated: ${new Date().toISOString()}

- Accepted ids found in spike.log: ${total}
- Reached a final status (DONE or FAILED): ${done}
- Of those, status was FAILED (not DONE): ${failedStatus}
- Never reached a final status within ${CONFIRM_TIMEOUT_S}s (lost/stuck): ${lost}

## RF2 verdict

**${lost === 0 && total > 0 ? 'PASS' : total === 0 ? 'NO DATA' : 'FAIL'}** — ${
    lost === 0
      ? `no request was lost: ${done}/${total} accepted uploads reached DONE/FAILED.`
      : `${lost}/${total} accepted uploads never reached a final status — investigate before treating RF2 as satisfied.`
  }
`;

  const stdout = `CONFIRM PHASE SUMMARY
accepted_ids=${total} confirmed_final=${done} confirmed_failed_status=${failedStatus} lost=${lost}
`;

  return {
    stdout,
    [`${OUT_DIR}/confirm-summary.json`]: JSON.stringify(data, null, 2),
    [`${OUT_DIR}/confirm-report.md`]: md,
  };
}
