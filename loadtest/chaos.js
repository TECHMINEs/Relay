// Relay chaos / failover load test
// Run: k6 run loadtest/chaos.js --summary-export loadtest/results/chaos.json
//
// Scenario: three phases controlled by MOCK_ERROR_RATE env var.
//   Phase 1 (0–30 s)  – normal traffic, 0% errors.
//   Phase 2 (30–60 s) – chaos: mock provider forced to 100% errors.
//                        The circuit breaker should open; requests get 503.
//   Phase 3 (60–90 s) – recovery: mock back to 0% errors.
//                        The circuit breaker should enter HalfOpen then Closed;
//                        success rate should return to 100%.
//
// Because k6 cannot change the server's environment mid-run, we simulate the
// error-rate flip by calling a dedicated /chaos control endpoint that the
// gateway exposes when RELAY_CHAOS_API=true.  In the local Docker setup,
// we instead use a mock query parameter that the mock provider reads.
//
// NOTE: This script drives the mock provider directly (no real LLM calls).
//       Set RELAY_URL to the gateway base URL before running.

import http from 'k6/http';
import { check, sleep, group } from 'k6';
import { Counter, Rate, Trend } from 'k6/metrics';

// ─── Custom metrics ───────────────────────────────────────────────────────────
const successRate   = new Rate('relay_success_rate');
const phase1Success = new Rate('relay_phase1_success');  // baseline
const phase2Success = new Rate('relay_phase2_success');  // chaos window
const phase3Success = new Rate('relay_phase3_success');  // recovery
const chatLatency   = new Trend('relay_chaos_latency_ms', true);
const circuitOpen   = new Counter('relay_circuit_open_responses'); // 503s

// ─── Config ───────────────────────────────────────────────────────────────────
const BASE_URL   = __ENV.RELAY_URL   || 'http://localhost:8080';
// Control URL for flipping mock error rate (see internal/api/chaos_handler.go).
// When empty, the script skips the flip and observes whatever rate is set.
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
    // Phase 1 (baseline) must be 100% successful.
    relay_phase1_success: ['rate>0.99'],
    // Phase 3 (recovery) must recover to at least 95% success.
    relay_phase3_success: ['rate>0.95'],
    // Overall success across all phases must stay above 60%
    // (chaos window is 1/3 of runtime).
    relay_success_rate:   ['rate>0.60'],
  },
  summaryTrendStats: ['avg', 'min', 'med', 'max', 'p(90)', 'p(95)', 'p(99)'],
};

// ─── Helpers ─────────────────────────────────────────────────────────────────
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

// ─── Virtual User ────────────────────────────────────────────────────────────
export default function (data) {
  const start = (data && data.startMs) || __ENV.START_MS || Date.now();
  const elapsed = (Date.now() - start) / 1000;

  if (elapsed < 30) {
    // ── Phase 1: baseline ──────────────────────────────────────────────────
    group('phase1_baseline', () => {
      const ok = sendChat('phase1');
      phase1Success.add(ok);
    });
  } else if (elapsed < 60) {
    // ── Phase 2: chaos ────────────────────────────────────────────────────
    if (!errorFlipped) {
      setErrorRate(1.0);
      errorFlipped = true;
    }
    group('phase2_chaos', () => {
      const ok = sendChat('phase2');
      phase2Success.add(ok);
    });
  } else {
    // ── Phase 3: recovery ─────────────────────────────────────────────────
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

// ─── Setup / teardown ────────────────────────────────────────────────────────
export function setup() {
  // Stamp the absolute start time so VUs can compute elapsed seconds.
  const startMs = Date.now();
  // Ensure mock provider is in normal mode.
  setErrorRate(0);
  console.log(`Chaos test started at ${new Date(startMs).toISOString()}`);
  return { startMs };
}

export function teardown(_data) {
  // Always restore normal mode when the test ends.
  setErrorRate(0);
  console.log('Chaos test complete — mock provider restored to 0% errors');
}
