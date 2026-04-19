package session

import (
	"encoding/json"
	"testing"

	"github.com/DocumentDrivenDX/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"pgregory.net/rapid"
)

func genTokenUsage() *rapid.Generator[agent.TokenUsage] {
	return rapid.Custom(func(t *rapid.T) agent.TokenUsage {
		input := rapid.IntRange(0, 1_000_000).Draw(t, "input")
		output := rapid.IntRange(0, 1_000_000).Draw(t, "output")
		return agent.TokenUsage{Input: input, Output: output, Total: input + output}
	})
}

func genEvent() *rapid.Generator[agent.Event] {
	return rapid.Custom(func(t *rapid.T) agent.Event {
		eventTypes := []agent.EventType{
			agent.EventSessionStart, agent.EventLLMRequest,
			agent.EventLLMResponse, agent.EventToolCall, agent.EventSessionEnd,
		}
		return agent.Event{
			SessionID: rapid.StringMatching(`s-[a-z0-9]{8}`).Draw(t, "sid"),
			Seq:       rapid.IntRange(0, 10000).Draw(t, "seq"),
			Type:      eventTypes[rapid.IntRange(0, len(eventTypes)-1).Draw(t, "type_idx")],
			Data:      json.RawMessage(`{"key":"value"}`),
		}
	})
}

func TestProperty_EventMarshalRoundTrip(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		event := genEvent().Draw(t, "event")

		data, err := json.Marshal(event)
		require.NoError(t, err)

		var decoded agent.Event
		err = json.Unmarshal(data, &decoded)
		require.NoError(t, err)

		assert.Equal(t, event.SessionID, decoded.SessionID)
		assert.Equal(t, event.Seq, decoded.Seq)
		assert.Equal(t, event.Type, decoded.Type)
	})
}

func TestProperty_TokenUsageAddCommutative(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		a := genTokenUsage().Draw(t, "a")
		b := genTokenUsage().Draw(t, "b")

		// a + b
		ab := a
		ab.Add(b)

		// b + a
		ba := b
		ba.Add(a)

		assert.Equal(t, ab.Input, ba.Input)
		assert.Equal(t, ab.Output, ba.Output)
		assert.Equal(t, ab.Total, ba.Total)
	})
}

func TestProperty_TokenUsageAddAssociative(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		a := genTokenUsage().Draw(t, "a")
		b := genTokenUsage().Draw(t, "b")
		c := genTokenUsage().Draw(t, "c")

		// (a + b) + c
		ab := a
		ab.Add(b)
		abc := ab
		abc.Add(c)

		// a + (b + c)
		bc := b
		bc.Add(c)
		abc2 := a
		abc2.Add(bc)

		assert.Equal(t, abc.Input, abc2.Input)
		assert.Equal(t, abc.Output, abc2.Output)
		assert.Equal(t, abc.Total, abc2.Total)
	})
}

func TestProperty_PricingMonotonic(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		input1 := rapid.IntRange(0, 1_000_000).Draw(t, "input1")
		output1 := rapid.IntRange(0, 1_000_000).Draw(t, "output1")
		extra := rapid.IntRange(0, 100_000).Draw(t, "extra")

		model := "gpt-4o" // known model with non-zero pricing

		cost1 := DefaultPricing.EstimateCost(model, input1, output1)
		cost2 := DefaultPricing.EstimateCost(model, input1+extra, output1)
		cost3 := DefaultPricing.EstimateCost(model, input1, output1+extra)

		assert.GreaterOrEqual(t, cost2, cost1, "more input tokens should not decrease cost")
		assert.GreaterOrEqual(t, cost3, cost1, "more output tokens should not decrease cost")
	})
}

func TestProperty_SessionLogRoundTrip(t *testing.T) {
	dir := t.TempDir()
	rapid.Check(t, func(t *rapid.T) {
		sessionID := rapid.StringMatching(`s-[a-z0-9]{6}`).Draw(t, "sid")
		nEvents := rapid.IntRange(1, 20).Draw(t, "n_events")

		logger := NewLogger(dir, sessionID)
		for i := range nEvents {
			logger.Emit(agent.EventToolCall, ToolCallData{
				Tool:       "read",
				Input:      json.RawMessage(`{"path":"test.go"}`),
				Output:     rapid.String().Draw(t, "output"),
				DurationMs: int64(i),
			})
		}
		require.NoError(t, logger.Close())

		events, err := ReadEvents(dir + "/" + sessionID + ".jsonl")
		require.NoError(t, err)
		assert.Len(t, events, nEvents)

		for i, e := range events {
			assert.Equal(t, sessionID, e.SessionID)
			assert.Equal(t, i, e.Seq)
			assert.Equal(t, agent.EventToolCall, e.Type)
		}
	})
}
