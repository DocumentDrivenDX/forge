package harnesses

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseQuotaOutputBasic(t *testing.T) {
	output := "83% of 5h limit"
	info := ParseQuotaOutput(output)
	require.NotNil(t, info)
	assert.Equal(t, 83, info.PercentUsed)
	assert.Equal(t, "5h", info.LimitWindow)
	assert.Empty(t, info.ResetDate)
}

func TestParseQuotaOutputWithResetDate(t *testing.T) {
	output := "75% of 7 day limit, resets April 12"
	info := ParseQuotaOutput(output)
	require.NotNil(t, info)
	assert.Equal(t, 75, info.PercentUsed)
	assert.Equal(t, "7 day", info.LimitWindow)
	assert.Equal(t, "April 12", info.ResetDate)
}

func TestParseQuotaOutputWithParenResetDate(t *testing.T) {
	output := "83% of 5h limit (resets April 12)"
	info := ParseQuotaOutput(output)
	require.NotNil(t, info)
	assert.Equal(t, 83, info.PercentUsed)
	assert.Equal(t, "5h", info.LimitWindow)
}

func TestParseQuotaOutputNoMatch(t *testing.T) {
	output := "no quota information here"
	info := ParseQuotaOutput(output)
	assert.Nil(t, info)
}

func TestParseQuotaOutputEmpty(t *testing.T) {
	info := ParseQuotaOutput("")
	assert.Nil(t, info)
}

func TestParseQuotaOutputMultiline(t *testing.T) {
	output := "some header\n83% of 5h limit\nsome footer"
	info := ParseQuotaOutput(output)
	require.NotNil(t, info)
	assert.Equal(t, 83, info.PercentUsed)
}

func TestQuotaStateFromUsedPercent(t *testing.T) {
	assert.Equal(t, "blocked", QuotaStateFromUsedPercent(95))
	assert.Equal(t, "blocked", QuotaStateFromUsedPercent(100))
	assert.Equal(t, "ok", QuotaStateFromUsedPercent(94))
	assert.Equal(t, "ok", QuotaStateFromUsedPercent(0))
	assert.Equal(t, "unknown", QuotaStateFromUsedPercent(-1))
}
