# Qwen3.6-27B cross-provider comparison

**Date:** 2026-04-27
**Harness:** `ddx-agent -preset minimal -json -provider X -model Y -p ...`
**Goal:** under our agent harness, compare a cloud baseline (openrouter qwen/qwen3.6-plus) against three local-inference runtimes serving Qwen3.6-27B-class weights (omlx on vidar/grendel, lucebox dflash on bragi).
**Tier:** Tier-2 grading harness (8 prompts, structured checkers on visible content).

## Headline

| Target | Pass rate | Mean total | Range | Mean output tok |
|---|---|---|---|---|
| openrouter qwen/qwen3.6-plus | **8/8 = 100%** | **4.9 s** | 4.0–7.7 s | 60.6 |
| vidar omlx Qwen3.6-27B-MLX-8bit | 8/8 = 100% | 16.0 s | 12.1–19.9 s | 79.6 |
| grendel-omlx Qwen3.6-27B-MLX-8bit | 8/8 = 100% | 33.7 s | 24.8–57.6 s | 69.0 |
| grendel-omlx Qwen3.6-27B-UD-MLX-4bit | 8/8 = 100% | 35.7 s | 23.5–54.4 s | 81.4 |
| bragi lucebox Qwen3.6-27B-Q4_K_M | 8/8 = 100% | 114.8 s | 11.1–168.0 s | 88.5 |

**Quality:** all 5 targets correctly answered all 8 prompt categories (factual, math, reasoning-math, simple-instruction, code-py, json-out, structured-tool-shape, code-bug). The agent harness frames Qwen3.6 well enough that quality variance across these targets is not the discriminator.

**Speed: cloud ≫ vidar (omlx) ≫ grendel (omlx) ≈ grendel-4bit (omlx) ≫ lucebox.**

## Per-prompt detail

```
target                      category               total    out
openrouter qwen3.6-plus     factual                4.0s    28
openrouter qwen3.6-plus     math                   4.3s    28
openrouter qwen3.6-plus     reasoning-math         7.7s   157
openrouter qwen3.6-plus     simple-instruction     4.2s    45
openrouter qwen3.6-plus     code-py                5.6s   109
openrouter qwen3.6-plus     json-out               4.6s    38
openrouter qwen3.6-plus     structured-tool-shape  4.3s    60
openrouter qwen3.6-plus     code-bug               4.4s    60

vidar omlx 8bit             factual               12.1s    24
vidar omlx 8bit             math                  13.1s    43
vidar omlx 8bit             reasoning-math        19.9s   132
vidar omlx 8bit             simple-instruction    19.6s   133
vidar omlx 8bit             code-py               18.4s   107
vidar omlx 8bit             json-out              13.4s    44
vidar omlx 8bit             structured-tool-shape 15.2s    65
vidar omlx 8bit             code-bug              16.5s    89

grendel-omlx-8bit           factual               24.8s    29
grendel-omlx-8bit           math                  25.1s    22
grendel-omlx-8bit           reasoning-math        37.9s   120
grendel-omlx-8bit           simple-instruction    57.6s   110
grendel-omlx-8bit           code-py               33.9s    89
grendel-omlx-8bit           json-out              25.8s    27
grendel-omlx-8bit           structured-tool-shape 30.7s    67
grendel-omlx-8bit           code-bug              33.6s    88

grendel-omlx-4bit-UD        factual               54.4s    29
grendel-omlx-4bit-UD        math                  27.3s    63
grendel-omlx-4bit-UD        reasoning-math        37.2s   150
grendel-omlx-4bit-UD        simple-instruction    51.8s   113
grendel-omlx-4bit-UD        code-py               30.8s    89
grendel-omlx-4bit-UD        json-out              23.5s    27
grendel-omlx-4bit-UD        structured-tool-shape 28.3s    71
grendel-omlx-4bit-UD        code-bug              32.3s   109

bragi lucebox q4_k_m        factual               11.1s    37
bragi lucebox q4_k_m        math                  31.8s    47
bragi lucebox q4_k_m        reasoning-math       115.3s   159
bragi lucebox q4_k_m        simple-instruction   123.2s    45
bragi lucebox q4_k_m        code-py              164.4s   159
bragi lucebox q4_k_m        json-out             148.1s    60
bragi lucebox q4_k_m        structured-tool-shape 168.0s    90
bragi lucebox q4_k_m        code-bug             156.9s   111
```

Inputs were uniformly ~3500 tokens — the agent's minimal-preset overhead (system prompt + tools + scaffold) dominates, and is identical across targets. Cost-per-prompt comparison is therefore on output tokens + provider rate.

## Observations

### 1. Cloud is 3-23× faster than every local target

The cloud baseline (openrouter Alibaba-hosted qwen3.6-plus) is **~3.3× faster than vidar**, ~7× faster than grendel, ~23× faster than lucebox on this set. Expected — the cloud Qwen-plus runs on optimized inference infra. Not a fair single-machine comparison, but it sets the practical ceiling and the realistic floor for local inference latency.

### 2. vidar omlx is 2× faster than grendel omlx, same model

Same `Qwen3.6-27B-MLX-8bit` weights, same provider type (omlx). vidar mean 16.0 s vs grendel mean 33.7 s. Hardware difference — vidar's M-class is meaningfully stronger than grendel's M1 Max 64GB. If a single host is the goal, vidar is the obvious local pick.

### 3. 4bit-UD didn't speed grendel up vs 8bit through the agent

Through the wire-shape probe earlier, grendel 4bit-UD measured ~9.3 tok/s vs 8bit ~7.9 tok/s — a real per-token throughput delta. Through the agent path, both quants land in the same wall-clock band (33.7 s vs 35.7 s mean). The agent's per-prompt cycle (cold weights, full system-prompt re-encoding, thinking-mode reasoning trace) amortizes the per-token throughput difference. **Quantization choice on grendel doesn't appear to be the lever for our harness.**

### 4. Lucebox is the slowest, with high per-prompt variance

Lucebox mean 114.8 s, range 11.1–168.0 s — the second-slowest single prompt is 11× slower than the fastest. This is surprising for a project marketed as speculative decoding for throughput; possible factors:

- DFlash speculative decoding optimizes for token throughput once warm, not first-token latency. Each agent invocation re-loads / re-warms.
- Qwen3.6 thinking-mode reasoning is verbose; lucebox's reasoning_content emission may be slower than the actual answer.
- Earlier in the day this server got wedged twice under sustained load; a partially-degraded post-restart state can't be ruled out.

Worth a re-run on a quiet server to see if the variance tightens.

### 5. Lucebox passes all 8 categories under the agent path

Earlier the wire-shape probes saw lucebox return empty content (server wedge). Through the agent harness with the minimal preset and proper retry semantics, all 8 prompts now pass with sensible output. **The agent path is more robust to transient server behavior than direct curl probes** — exactly what we'd hope for. The earlier 0/8 result was a server-state artifact, not a harness incompatibility.

## What would change the picture

- **Loaded comparison.** All numbers above are per-prompt cold-cycle. A multi-turn conversation would let lucebox's speculative-decoding warm-state advantage show; right now we're in its worst case. Adding a "warm-state" subset (5 turns reusing kv-cache) would help.
- **Larger output (long-form).** Our prompts cap output around 100-160 tokens. lucebox's claimed advantage (>200 tok/s on the upstream HumanEval bench) may only show on longer generations.
- **Tool-heavy workload.** The agent's tool-loop wasn't exercised here (the prompts didn't need tools). lucebox's tool-call gaps (filed in `lucebox-tool-support-2026-04-27.md`) only matter on tool-heavy work; quality numbers above don't reflect that.

## Take-aways for harness use today

1. **Production local pick: vidar omlx 8bit.** 100% pass-rate, 16 s mean per prompt, fastest of the local options.
2. **Backup local pick: grendel omlx (either quant).** Same quality, ~2× slower on weaker hardware. Use when vidar is unavailable.
3. **Lucebox is not yet a production option** in our harness — both because of the tool-calling gaps already reported and because per-prompt latency under our cycle is too high. Re-evaluate after the upstream tool-choice fix and after a sustained-warm-state probe.
4. **Cloud is the speed ceiling.** When latency matters more than privacy/cost, openrouter qwen3.6-plus is 3-7× faster than any local option here.

## Next moves

- File a follow-up bead to add a warm-state / multi-turn subset to the Tier-2 harness so lucebox's speculative-decoding case can be measured fairly.
- Re-run after lucebox upstream lands the `tool_choice` fix from `docs/research/lucebox-tool-support-2026-04-27.md` Gap 1.
- Tier 3 (beadbench preflight) on the same target set when timeline permits — that's where actual coding-task completion (not just one-shot grading) gets measured.
