-- token_bucket.lua — Atomic token bucket with elapsed-time refill.
--
-- This script runs atomically on the Redis server, preventing any race
-- condition between reading, computing, and writing the bucket state.
-- No other Redis command from any client can execute between these steps.
--
-- KEYS[1] : hash key for this API key's bucket state, e.g. "rl:bucket:<hash>"
-- ARGV[1] : rate_per_second   (float, tokens replenished per second)
-- ARGV[2] : burst             (integer, max bucket capacity)
-- ARGV[3] : cost              (integer, tokens this request costs, usually 1)
-- ARGV[4] : now_ms            (integer, current Unix time in milliseconds)
-- ARGV[5] : ttl_seconds       (integer, key TTL so idle keys expire)
--
-- Returns: { allowed, remaining_tokens_floor, retry_after_ms, reset_ms }
--   allowed             : 1 if request is permitted, 0 if rate-limited
--   remaining_tokens_floor : floor of remaining tokens after this call
--   retry_after_ms      : milliseconds until `cost` tokens are available (0 if allowed)
--   reset_ms            : Unix ms timestamp when bucket reaches burst capacity

local key        = KEYS[1]
local rate       = tonumber(ARGV[1])   -- tokens/second
local burst      = tonumber(ARGV[2])   -- max capacity
local cost       = tonumber(ARGV[3])   -- tokens this call consumes
local now_ms     = tonumber(ARGV[4])   -- current time ms
local ttl_s      = tonumber(ARGV[5])   -- key TTL in seconds

-- Read existing bucket state (stored as two hash fields).
local stored = redis.call('HMGET', key, 'tokens', 'last_ms')
local tokens  = tonumber(stored[1])
local last_ms = tonumber(stored[2])

-- Initialise bucket on first call.
if tokens == nil or last_ms == nil then
    tokens  = burst
    last_ms = now_ms
end

-- Refill: compute how many tokens have accrued since last update.
-- elapsed_ms can be negative if clocks differ; clamp to zero.
local elapsed_ms = math.max(0, now_ms - last_ms)
local refill     = elapsed_ms / 1000.0 * rate

-- Cap at burst capacity.
tokens = math.min(burst, tokens + refill)

-- Milliseconds until bucket would have `burst` tokens again (for reset header).
local fill_to_full_ms = math.ceil((burst - tokens) / rate * 1000)
local reset_ms = now_ms + fill_to_full_ms

local allowed       = 0
local retry_after_ms = 0

if tokens >= cost then
    -- Allow and deduct.
    tokens  = tokens - cost
    allowed = 1
else
    -- Compute when `cost` tokens will be available.
    local deficit = cost - tokens
    retry_after_ms = math.ceil(deficit / rate * 1000)
end

-- Persist updated state and refresh TTL.
redis.call('HMSET', key,
    'tokens', tostring(tokens),
    'last_ms', tostring(now_ms))
redis.call('EXPIRE', key, ttl_s)

return {allowed, math.floor(tokens), retry_after_ms, reset_ms}
