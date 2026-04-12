package agent

import "errors"

// ErrCompactionNoFit reports that compaction was needed but could not produce
// a message history that fits within the effective context window.
var ErrCompactionNoFit = errors.New("agent: compaction could not fit within the effective context window")

// ErrReasoningOverflow is returned by consumeStream when the model has emitted
// more than reasoningByteLimit bytes of pure reasoning_content without
// producing any content or tool_call deltas. The model is stuck in a runaway
// reasoning loop and the stream is aborted early.
var ErrReasoningOverflow = errors.New("agent: reasoning overflow: model produced only reasoning tokens past byte limit")

// ErrReasoningStall is returned by consumeStream when only reasoning_content
// deltas have arrived for longer than reasoningStallTimeout with no content or
// tool_call delta. The model appears to be making no forward progress.
var ErrReasoningStall = errors.New("agent: reasoning stall: model produced only reasoning tokens past stall timeout")

// ErrToolCallLoop reports that the agent produced identical tool calls for
// toolCallLoopLimit consecutive turns, indicating a non-converging loop.
var ErrToolCallLoop = errors.New("agent: identical tool calls repeated, aborting loop")
