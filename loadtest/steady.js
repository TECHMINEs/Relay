// Steady-state load test.
// k6 run loadtest/steady.js --summary-export loadtest/results/steady.json
//
// Scenario: 30 VUs ramp up over 10s, hold for 60s, ramp down over 10s.
// Target: mock provider (no real upstream needed).
// Asserts: p99 < 500ms, error rate < 1%.

import http from 'k6/http';
import { check, sleep } from 'k6';
import { Trend, Counter, Rate } from 'k6/metrics';
const chatLatency   = new Trend('relay_chat_latency_ms',   true);
const embedLatency  = new Trend('relay_embed_latency_ms',  true);
const errorCount    = new Counter('relay_errors');
const errorRate     = new Rate('relay_error_rate');

const BASE_URL = __ENV.RELAY_URL || 'http://localhost:8080';

export const options = {
  scenarios: {
    steady_chat: {
      executor: 'ramping-vus',
      startVUs: 0,
      stages: [
        { duration: '10s', target: 30 },
        { duration: '60s', target: 30 },
        { duration: '10s', target: 0  },
      ],
      gracefulRampDown: '5s',
    },
  },
  thresholds: {
    // Gate: 99th-percentile chat latency must stay below 500 ms.
    relay_chat_latency_ms: ['p(99)<500'],
    // Gate: fewer than 1% of requests may error.
    relay_error_rate:       ['rate<0.01'],
    // Standard k6 HTTP error threshold (4xx/5xx).
    http_req_failed:        ['rate<0.01'],
  },
  summaryTrendStats: ['avg', 'min', 'med', 'max', 'p(90)', 'p(95)', 'p(99)'],
};
const chatPayload = JSON.stringify({
  model: 'gpt-4',
  messages: [{ role: 'user', content: 'Hello from k6 steady load test' }],
});

const embedPayload = JSON.stringify({
  model: 'text-embedding-3-small',
  input: ['k6 embedding test'],
});

const headers = { 'Content-Type': 'application/json' };
export default function () {
  // 80% chat, 20% embeddings — mirrors typical LLM gateway traffic.
  if (Math.random() < 0.8) {
    const res = http.post(`${BASE_URL}/v1/chat/completions`, chatPayload, { headers });
    const ok  = check(res, {
      'chat status 200':          (r) => r.status === 200,
      'chat has choices':         (r) => {
        try { return JSON.parse(r.body).choices.length > 0; } catch { return false; }
      },
    });
    chatLatency.add(res.timings.duration);
    errorRate.add(!ok);
    if (!ok) errorCount.add(1);
  } else {
    const res = http.post(`${BASE_URL}/v1/embeddings`, embedPayload, { headers });
    const ok  = check(res, {
      'embed status 200':         (r) => r.status === 200,
      'embed has data':           (r) => {
        try { return JSON.parse(r.body).data.length > 0; } catch { return false; }
      },
    });
    embedLatency.add(res.timings.duration);
    errorRate.add(!ok);
    if (!ok) errorCount.add(1);
  }

  sleep(0.1); // 100 ms think-time per VU → max theoretical 300 RPS at 30 VUs
}
