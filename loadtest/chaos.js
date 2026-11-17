// Chaos / failover load test.
// k6 run loadtest/chaos.js --summary-export loadtest/results/chaos.json
//
// 0-30s:  baseline, 0% upstream errors.
// 30-60s: the mock provider fails every call; the breaker should open (503s).
// 60-90s: errors back to 0%; the breaker should half-open, then close.
//
// The error rate is flipped through the gateway's /__chaos endpoint.
// Set RELAY_URL to the gateway base URL.

import http from 'k6/http';
import { check, sleep, group } from 'k6';
import { Counter, Rate, Trend } from 'k6/metrics';
const successRate   = new Rate('relay_success_rate');
const phase1Success = new Rate('relay_phase1_success');  // baseline
const phase2Success = new Rate('relay_phase2_success');  // chaos window
const phase3Success = new Rate('relay_phase3_success');  // recovery
const chatLatency   = new Trend('relay_chaos_latency_ms', true);
const circuitOpen   = new Counter('relay_circuit_open_responses'); // 503s

const BASE_URL   = __ENV.RELAY_URL   || 'http://localhost:8080';
const CHAOS_URL  = __ENV.CHAOS_URL  || `${BASE_URL}/__chaos`;

export const options = {
  scenarios: {
    chaos_traffic: {
      executor: 'constant-vus',
      vus: 20,
      duration: '90s',
    },
  },
  thresholds: {
    relay_phase1_success: ['rate>0.99'],
    relay_phase3_success: ['rate>0.95'],
    // The chaos window is a third of the run.
    relay_success_rate:   ['rate>0.60'],
  },
  summaryTrendStats: ['avg', 'min', 'med', 'max', 'p(90)', 'p(95)', 'p(99)'],
};
const chatPayload = JSON.stringify({
  model: 'gpt-4',
  messages: [{ role: 'user', content: 'chaos test request' }],
});
const headers = { 'Content-Type': 'application/json' };

function sendChat(phase) {
  const res = http.post(`${BASE_URL}/v1/chat/completions`, chatPayload, { headers, tags: { phase } });
  const ok  = res.status === 200;
  const is503 = res.status === 503;

  check(res, { [`${phase} status ok-or-known`]: (r) => r.status === 200 || r.status === 503 || r.status === 429 });
  chatLatency.add(res.timings.duration);
  successRate.add(ok);
  if (is503) circuitOpen.add(1);
  return ok;
}

function setErrorRate(rate) {
  // POST to the chaos control endpoint; ignore errors if it's not available.
  try {
    http.post(CHAOS_URL, JSON.stringify({ error_rate: rate }), { headers, timeout: '2s' });
  } catch (_) { /* chaos endpoint optional */ }
}

let errorFlipped = false;
let restored = false;
export default function (data) {
  const start = (data && data.startMs) || __ENV.START_MS || Date.now();
  const elapsed = (Date.now() - start) / 1000;

  if (elapsed < 30) {
    group('phase1_baseline', () => {
      const ok = sendChat('phase1');
      phase1Success.add(ok);
    });
  } else if (elapsed < 60) {
    if (!errorFlipped) {
      setErrorRate(1.0);
      errorFlipped = true;
    }
    group('phase2_chaos', () => {
      const ok = sendChat('phase2');
      phase2Success.add(ok);
    });
  } else {
    if (!restored) {
      setErrorRate(0);
      restored = true;
    }
    group('phase3_recovery', () => {
      const ok = sendChat('phase3');
      phase3Success.add(ok);
    });
  }

  sleep(0.05); // 50 ms think-time → max ~400 RPS at 20 VUs
}
export function setup() {
  const startMs = Date.now();
  setErrorRate(0);
  console.log(`Chaos test started at ${new Date(startMs).toISOString()}`);
  return { startMs };
}

export function teardown(_data) {
  setErrorRate(0);
  console.log('Chaos test complete — mock provider restored to 0% errors');
}
