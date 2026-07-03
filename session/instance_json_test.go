package session

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestKind_MarshalJSON_String proves the wire renders Kind as a
// self-documenting string, not an opaque int. A consumer parsing
// 'list_instances' sees "kind": "orchestrator" instead of 1. Resolves
// finding #2 (enums exposed as raw ints).
func TestKind_MarshalJSON_String(t *testing.T) {
	b, err := json.Marshal(KindOrchestrator)
	require.NoError(t, err)
	assert.Equal(t, `"orchestrator"`, string(b))

	b, err = json.Marshal(KindWorker)
	require.NoError(t, err)
	assert.Equal(t, `"worker"`, string(b))
}

// TestKind_UnmarshalJSON_AcceptsStringOrInt proves the wire accepts BOTH a
// string ("orchestrator") and an int (1). The CLI passes strings; a raw
// JSON-RPC caller may pass either. Resolves finding #8 (string 'kind'
// rejected with 'cannot unmarshal string into Go struct field ...').
func TestKind_UnmarshalJSON_AcceptsStringOrInt(t *testing.T) {
	cases := []struct {
		in   string
		want Kind
	}{
		{`"worker"`, KindWorker},
		{`"orchestrator"`, KindOrchestrator},
		{`"orch"`, KindOrchestrator}, // alias accepted
		{`0`, KindWorker},
		{`1`, KindOrchestrator},
	}
	for _, c := range cases {
		var k Kind
		require.NoError(t, json.Unmarshal([]byte(c.in), &k), "input %s", c.in)
		assert.Equal(t, c.want, k, "input %s", c.in)
	}
}

// TestKind_UnmarshalJSON_RejectsInvalid proves a bad value errors cleanly
// rather than silently defaulting — a consumer must learn its input was wrong.
func TestKind_UnmarshalJSON_RejectsInvalid(t *testing.T) {
	var k Kind
	require.Error(t, json.Unmarshal([]byte(`"bogus"`), &k))
	require.Error(t, json.Unmarshal([]byte(`99`), &k))
}

// TestStatus_MarshalJSON_String proves Status renders as a string on the wire
// ("running"/"ready"/"loading"/"paused"), not an int. Resolves finding #2.
func TestStatus_MarshalJSON_String(t *testing.T) {
	cases := []struct {
		s    Status
		want string
	}{
		{Running, `"running"`},
		{Ready, `"ready"`},
		{Loading, `"loading"`},
		{Paused, `"paused"`},
	}
	for _, c := range cases {
		b, err := json.Marshal(c.s)
		require.NoError(t, err)
		assert.Equal(t, c.want, string(b))
	}
}

// TestStatus_UnmarshalJSON_AcceptsStringOrInt mirrors TestKind for Status.
func TestStatus_UnmarshalJSON_AcceptsStringOrInt(t *testing.T) {
	cases := []struct {
		in   string
		want Status
	}{
		{`"running"`, Running},
		{`"ready"`, Ready},
		{`"loading"`, Loading},
		{`"paused"`, Paused},
		{`0`, Running},
		{`1`, Ready},
		{`2`, Loading},
		{`3`, Paused},
	}
	for _, c := range cases {
		var s Status
		require.NoError(t, json.Unmarshal([]byte(c.in), &s), "input %s", c.in)
		assert.Equal(t, c.want, s, "input %s", c.in)
	}
}

// TestKind_RoundTripInInstanceData proves the end-to-end persistence path:
// an InstanceData with Kind=Orchestrator serialises to JSON with the string
// form and deserialises back losslessly. This is what a daemon restart does.
func TestKind_RoundTripInInstanceData(t *testing.T) {
	orig := InstanceData{Title: "o", Kind: KindOrchestrator, Status: Paused}
	b, err := json.Marshal(orig)
	require.NoError(t, err)
	// The serialised form uses strings for the enums.
	assert.Contains(t, string(b), `"kind":"orchestrator"`)
	assert.Contains(t, string(b), `"status":"paused"`)

	var back InstanceData
	require.NoError(t, json.Unmarshal(b, &back))
	assert.Equal(t, KindOrchestrator, back.Kind)
	assert.Equal(t, Paused, back.Status)
}
