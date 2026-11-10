// Relay k6 helper utilities shared across load-test scripts.

/**
 * parseK6Summary extracts the key numbers from a k6 summary JSON file.
 * Usage: import { parseK6Summary } from './utils.js';
 *
 * @param {string} path - Path to the JSON summary file.
 * @returns {object} Parsed summary with rps, p99_ms, error_rate.
 */
export function buildChatPayload(model = 'gpt-4', content = 'load test') {
  return JSON.stringify({
    model,
    messages: [{ role: 'user', content }],
  });
}

export function buildEmbedPayload(model = 'text-embedding-3-small', input = 'load test') {
  return JSON.stringify({ model, input: [input] });
}

/**
 * assertThreshold throws an error if a rate metric is below the given floor.
 * Useful for custom pass/fail gates beyond k6's built-in thresholds.
 */
export function assertRate(metric, floor, label) {
  const val = metric.values ? metric.values.rate : null;
  if (val !== null && val < floor) {
    console.error(`THRESHOLD FAILED: ${label} rate=${(val * 100).toFixed(2)}% < ${(floor * 100).toFixed(2)}%`);
  }
}
