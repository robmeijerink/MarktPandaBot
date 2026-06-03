# TASK: Non-Destructive Two-Stage Liquidation Scoring Upgrade

## CONTEXT
We are upgrading an existing Go cryptocurrency trading bot. It already aggregates
WebSocket streams from **Bybit and OKX** (liquidations, Open Interest, funding,
price ranges) and sends Telegram liquidation alerts. We are adding a two-stage
scoring layer on top of the EXISTING alert, plus an in-memory rolling baseline and
a warm-boot routine. We must not alter or delete any existing alert logic or data
structures.

This document contains LOCKED design decisions. Where a value is marked
`# TUNABLE`, expose it as a config constant — do not hardcode it inline. Do NOT
invent thresholds that are not specified here; if something is genuinely
underspecified, follow the "Clarification" section instead of guessing.

---

## NON-NEGOTIABLE ARCHITECTURAL RULES
1. **APPEND ONLY.** The Setup Matrix is appended to the bottom of the existing
   Telegram alert string. Never modify, reorder, or remove existing fields.
2. **NO DISK I/O.** All historical data lives in memory (fixed-length ring buffer).
   No CSV, no DB, no files.
3. **TWO STAGES.**
   - **T0 (immediate):** compute Setup Matrix, append to the existing alert.
   - **T+N (delayed):** confirmation via candle-sync (see §6), sent as an
     INDEPENDENT new Telegram message.
4. **CANCELABLE.** A new liquidation alert cancels any pending confirmation
   (`context.WithCancel`) and starts a fresh one.
5. **ADDITIVE INTEGRATION.** New code goes in new files/structs where possible.
   Hook into existing logic at a single, clearly-commented injection point.

---

## LOCKED DESIGN DECISIONS (these resolve prior ambiguities — do not re-interpret)

**D1 — "Vol Spike" means TRADING volume, not liquidation volume.**
The 5×-median check compares the current 5-minute **aggregated trading volume**
(Bybit + OKX, quote/USD volume) against the median of the trailing buffer. This is
chosen specifically because trading volume is the only metric that can be hydrated
from the klines endpoint on boot. Liquidation magnitude is NOT used for the median.

**D2 — The T0 trigger is the EXISTING alert.**
Do not build a new trigger. Whenever the existing liquidation alert fires, compute
and append the Setup Matrix. The existing logic decides *whether* to alert; we only
*enrich* it.

**D3 — Confirmation only starts for meaningful setups.**
Start the T+N confirmation goroutine only if the T0 score >=
`START_CONFIRMATION_MIN_SCORE`. Low-score setups get the appended matrix but no
follow-up, to keep the feed quiet.

**D4 — All scoring weights and thresholds are TUNABLE defaults, not validated edges.**
Ship the defaults below, but they MUST be backtested before being trusted. Put them
in one config block.

**D5 — Multi-exchange aggregation is mandatory and consistent.**
Both the live volume bucket and the warm-boot hydration aggregate Bybit + OKX into
the same 5-minute time buckets (aligned to UTC boundaries). The baseline and the
live value must be computed identically, or the median is meaningless.

**D6 — All candle/time math is in UTC.** Assume the host clock is NTP-synced; note
this requirement in a comment. 5-minute boundaries are xx:00, xx:05, … UTC.

**D7 — Single direction only: long capitulation → reversal UP.**
Skew tests the LONG-liquidation share and reclaim tests close > range high. The
short-squeeze → reversal-DOWN mirror (short share, close < range low) is intentionally
OUT OF SCOPE — do not build it unless explicitly asked.

---

## CONFIGURATION (single source of truth)

```go
type Config struct {
    Exchanges            []string // ["bybit","okx"]
    PrimaryExchange      string   // "bybit" — used for the reclaim reference close
    BufferSize           int      // 288  (24h of 5-min buckets)
    MinBufferFill        int      // 144  — below this, Vol Spike scores 0 (still warming)

    // T0 Setup Matrix weights (TUNABLE)
    WeightOIDrop         int      // 3   — requires OI drop >= OIDropPct
    WeightSkew           int      // 2   — requires long-liquidation share >= SkewPct
    WeightVolSpike       int      // 1   — requires vol >= VolSpikeMult * median
    WeightFunding        int      // 1   — requires funding sign/trend pass (see §4)
    OIDropPct            float64  // 2.5
    SkewPct              float64  // 90.0
    VolSpikeMult         float64  // 5.0
    FundingNegThreshold  float64  // 0.0  — pass if current funding <= this
    FundingTrendDropPct  float64  // 30.0 — OR pass if funding fell this % over lookback
    FundingLookbackHours float64  // 1.0  — lookback for the trend test (NOT 5 min)

    MaxT0Score           int      // 7   (3+2+1+1) — DISPLAY denominator only; keep in sync with weights
    StartConfirmationMinScore int // 5   (TUNABLE) — D3 gate, evaluated on the ABSOLUTE score

    // Candle-sync (§6)
    MinLeadSeconds       int      // 180 — if next close is sooner, target the one after
    CandleIntervalSec    int      // 300

    // Warm boot (§4)
    KlineFetchTimeoutSec int      // 10
    KlineMaxRetries      int      // 3
}
```

---

## REQUIRED COMPONENTS

### 1. Exchange Aggregation Layer
A small helper that, given a UTC 5-minute bucket start time, returns the summed
quote-volume across all configured exchanges.
**Critical — identical definition on both paths.** The median is only meaningful if
the live value and the hydrated value are measured the same way. Use the **closed
candle's quote-volume from the klines endpoint for BOTH**. On each UTC 5-minute
boundary, take the just-closed kline's quote-volume per exchange, sum them, and
`Add()` to the ring (poll the single latest closed kline, or read it from a kline WS
subscription if one already exists). Do NOT mix a raw trade-stream sum on the live
path with kline volume on the boot path — the definitions differ and would silently
bias the spike check.

### 2. In-Memory Ring Buffer (thread-safe)
```go
type VolumeRing struct {
    mu   sync.RWMutex
    data []float64 // len == BufferSize, FIFO
    fill int       // number of real samples currently held
}
func (r *VolumeRing) Add(v float64)        // O(1), drops oldest
func (r *VolumeRing) Median() (float64, bool) // false if fill < MinBufferFill
func (r *VolumeRing) Fill() int
```
- The buffer holds COMPLETED 5-minute buckets only. The in-progress bucket is
  excluded from the median and added when it closes.
- `Median` returns `ok=false` while `fill < MinBufferFill`; in that case Vol Spike
  scores 0 and the matrix line shows `(warming up)`.
- All access mutex-guarded (write on bucket rollover, read on T0 evaluation).

### 3. Warm Boot (state hydration via REST)
On startup, BEFORE opening WebSocket connections:
1. For each exchange, fetch the last `BufferSize` closed 5-minute klines via
   `net/http` from the public klines endpoint.
2. **Pagination:** do NOT assume one request returns 288 candles. Respect each
   exchange's per-request limit (verify against current API docs; e.g. OKX commonly
   caps lower than Bybit) and page as needed.
3. Align both exchanges' candles by UTC bucket timestamp and SUM quote-volume per
   bucket (D5). Push the summed buckets into the ring in chronological order.
4. **Failure handling:** retry up to `KlineMaxRetries` with backoff. If still
   failing, log a warning and start with an empty/partial buffer — the
   `MinBufferFill` gate then keeps Vol Spike at 0 until enough live buckets
   accumulate. Never block startup indefinitely; never crash on a failed warm boot.

### 4. T0 Setup Matrix
On each existing alert, read the values the alert already computed (OI delta %,
long vs short liquidation split, funding rate/trend) plus the current bucket volume
vs `Median()`. Score with the configured weights:

| Item       | Pass condition                              | Score on pass |
|------------|---------------------------------------------|---------------|
| OI Drop    | OI change <= -OIDropPct over the window     | WeightOIDrop  |
| Skew       | long-liq share >= SkewPct                   | WeightSkew    |
| Vol Spike  | bucketVol >= VolSpikeMult * median (ok)     | WeightVolSpike|
| Funding    | funding flipped negative OR compressed hard | WeightFunding |

**Funding pass condition (corrected).** Funding rates settle on the exchange's own
cadence (typically every 1–8h), so a "5-minutes-ago" delta is meaningless — funding
barely moves in 5 minutes. Pass if **current funding <= `FundingNegThreshold`**
(funding has flipped to/below zero), OR — only if longer funding history is already
tracked — if funding fell by >= `FundingTrendDropPct` over `FundingLookbackHours`.
The absolute-sign test is always available at alert time, so Funding is never N/A and
`MaxT0Score` stays fixed at 7. (N/A handling applies only to the §6 confirmation flow
signals, which are optional.)

### 5. T0 String Formatter (append to existing alert)
```
------------------------
📊 SETUP MATRIX (T0)
{e} OI Drop (>={OIDropPct}%)   : {score}
{e} Skew (>={SkewPct}% long)   : {score}
{e} Vol Spike (>={VolSpikeMult}x) : {score}
{e} Funding Flip               : {score}

Score: {sum}/{max}   ({HIGH CONVICTION if sum>=StartConfirmationMinScore else LOW})
⏳ Monitoring absorption window…
```
`{e}` = ✅ on pass, ❌ on fail, ⚪ on N/A. Append ONLY; never edit lines above the
separator.

### 6. Dynamic Candle-Sync Confirmation
On a qualifying T0 (D3):
1. **Cancel** any running confirmation context, then create a fresh one
   (mutex-guarded `cancelFunc`, see §7).
2. **Capture T0 state** into the goroutine: flush range high (the liquidation range
   top **on `PrimaryExchange`** — same venue as the reclaim close, so the comparison
   is like-for-like), baseline price, and T0 UTC timestamp.
3. **Target time:**
   ```
   nextClose = ceilToNext5Min(nowUTC)            // strictly after now
   if (nextClose - nowUTC) < MinLeadSeconds:
       target = nextClose + CandleIntervalSec
   else:
       target = nextClose
   ```
   This yields an effective wait of ~3–8 minutes and guarantees evaluation on a
   fully-closed candle that began after the flush settled.
4. **At target**, evaluate the confirmation metrics, then send the message.
   Use `select { case <-time.After(until target): … ; case <-ctx.Done(): return }`.

**Confirmation metrics & data sources:**
- **Price Reclaim (required):** closing price of the target 5-min candle on
  `PrimaryExchange` > captured flush range high. Pass/fail.
- **CVD Inflow:** sum of signed taker volume over the target candle across configured
  perp trade streams: `+qty` for taker-buy, `-qty` for taker-sell (use quote/USD).
  Pass if net > 0.
- **Spot-vs-Perp:** net spot taker-buy volume over the window > 0 (spot demand
  absorbing the perp flush). Pass if net > 0.

**Data-source check (do this, do not assume):** CVD needs a perp aggregated-trade
stream with maker/taker side; Spot-vs-Perp needs a spot trade stream. Inspect the
existing subscriptions:
- If a required stream already exists, reuse it.
- If not, ADD the subscription (preferred).
- If it genuinely cannot be added, DEGRADE GRACEFULLY: render that metric as
  `⚪ N/A`, exclude it from the verdict, and proceed. Never fail the confirmation
  because one optional signal is missing.

**Verdict:** `Reversal confirmed` if Price Reclaim passes AND (CVD passes OR
Spot-vs-Perp passes); `Absorbed — weak` if Price Reclaim passes but both flow
signals fail/NA; `Not confirmed` if Price Reclaim fails.

### 7. Confirmation Message Formatter (independent message)
```
✅ CONFIRMATION (T+{minutesWaited}m, candle {hh:mm} UTC)
Pair: BTC/USDT | Close: ${close}
📊 Confirmation Matrix:
{e} Price Reclaim (> ${flushRangeHigh})
{e} CVD Inflow (net positive)
{e} Spot vs Perp (spot leading)

Verdict: {verdict}
```

---

## CONCURRENCY & CANCELLATION DISCIPLINE
- One `sync.Mutex` guards the active `cancelFunc`. On new qualifying T0: lock, call
  old `cancelFunc` if non-nil, create new context, store its cancel, unlock, then
  launch the goroutine.
- After a confirmation fires naturally, clear the stored `cancelFunc` (guarded) so a
  stale cancel is never called.
- The ring buffer uses its own `RWMutex` (§2). Do not share locks between the two.
- The goroutine must exit on `ctx.Done()` without sending anything.

---

## CALIBRATION & VALIDATION (read before trusting output)
This spec implements **plumbing only**. The weights, the 5× multiple, the 90% skew,
the 2.5% OI drop, and the score gate are reasonable starting points, NOT a validated
edge. Before acting on these alerts, the scoring rule (T0 setup AND the T+N reclaim)
should be backtested against historical flushes to measure how often "Reversal
confirmed" was actually followed by a usable move vs a dead-cat bounce. Keep every
threshold in `Config` so this tuning needs no code changes.

---

## TESTING REQUIREMENTS
Add unit tests (no network) for:
1. `ceilToNext5Min` + target-time logic: assert effective wait is always in
   [180s, 480s) across flush offsets 0–299s within a candle, including the
   `MinLeadSeconds` boundary.
2. `VolumeRing`: FIFO drop at capacity, median correctness, and `ok=false` below
   `MinBufferFill`.
3. T0 scoring: each item's pass/fail yields the expected sum against the fixed
   `MaxT0Score`, and the D3 gate fires on the ABSOLUTE score (not a ratio).
4. Cancellation: a second T0 cancels the first goroutine (use an injected clock /
   short interval) and only the second fires.

---

## EXECUTION INSTRUCTIONS
1. Review the workspace and locate: (a) the existing alert-generation function and
   its data struct, (b) existing exchange subscriptions, (c) any existing per-bucket
   volume tracking, (d) the funding/OI fields available at alert time.
2. Implement the components above in new files; hook in at a single commented
   injection point in the existing alert path. Do not touch existing fields.
3. Use only the standard library plus whatever HTTP/WebSocket libs the project
   already uses. REST via `net/http`.
4. **Surface your assumptions explicitly** in your summary, and ASK before coding if
   any of these are unclear: which klines endpoints/limits the project targets,
   whether perp aggregated-trade and spot-trade streams exist, the exact shape of the
   existing alert struct and Telegram formatting, and how OI-delta / funding are
   currently exposed. Do not guess on data-source availability — confirm or degrade
   per §6.
5. **Stop after implementation and passing tests.** Work on a dedicated feature
   branch, present the full diff and a short summary, and do NOT deploy or start the
   live bot. The alerts are un-validated until backtested (see Calibration).
