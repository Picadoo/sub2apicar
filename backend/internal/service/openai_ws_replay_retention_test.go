package service

import (
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Incremental turns can resend large tool schemas alongside a tiny input.
// Replay only needs the input items; retaining every complete request would
// make a long-lived connection accumulate copies of unrelated tool metadata.
func TestOpenAIWSReplayDoesNotRetainUnrelatedRequestFields(t *testing.T) {
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	history := buildReplayHistoryWithLargeToolSchemas(t)
	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	runtime.KeepAlive(history)

	require.Len(t, history, 24)
	for i, item := range history {
		require.JSONEq(t, fmt.Sprintf(`{"type":"message","role":"user","content":"turn-%d"}`, i), string(item))
	}
	retained := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	require.Less(t, retained, int64(4<<20), "a few KB of replay input must not pin 12 MiB of repeated tool schemas")
}

func buildReplayHistoryWithLargeToolSchemas(t *testing.T) []json.RawMessage {
	t.Helper()
	description := strings.Repeat("x", 512<<10)
	var history []json.RawMessage
	exists := false
	for turn := 0; turn < 24; turn++ {
		payload := []byte(fmt.Sprintf(`{"model":"gpt-6-astra","previous_response_id":"resp_previous","input":[{"type":"message","role":"user","content":"turn-%d"}],"tools":[{"type":"function","name":"inspect","description":"%s","parameters":{"type":"object"}}]}`, turn, description))
		var err error
		history, exists, err = buildOpenAIWSReplayInputSequence(history, exists, payload, turn > 0)
		require.NoError(t, err)
		require.True(t, exists)
	}
	return history
}
