package program

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPiAdapter_NameAndMatch(t *testing.T) {
	a := PiAdapter{}
	assert.Equal(t, "pi", a.Name())
	assert.True(t, a.Matches("pi"))
	assert.True(t, a.Matches("/opt/homebrew/bin/pi"))
	assert.True(t, a.Matches("/usr/local/bin/pi"))
	// Must not match look-alikes.
	assert.False(t, a.Matches("ping"))
	assert.False(t, a.Matches("claude"))
	assert.False(t, a.Matches("pixi"))
}

// A helper that builds a JSONL message entry line, so test cases stay readable.
func piEntry(role, stopReason string) string {
	return `{"type":"message","message":{"role":"` + role + `","stopReason":"` + stopReason + `"}}`
}

func TestPiAdapter_Detect(t *testing.T) {
	cases := []struct {
		name     string
		content  string
		wantStat Status
		wantKind PromptKind
	}{
		{
			name:     "assistant stop -> Ready",
			content:  piEntry("assistant", "stop"),
			wantStat: StatusReady,
			wantKind: PromptReady,
		},
		{
			name:     "assistant toolUse -> Working (mid-loop)",
			content:  piEntry("assistant", "toolUse"),
			wantStat: StatusWorking,
			wantKind: PromptNone,
		},
		{
			name:     "assistant length -> Working (hit length limit, likely mid-turn)",
			content:  piEntry("assistant", "length"),
			wantStat: StatusWorking,
			wantKind: PromptNone,
		},
		{
			name:     "assistant error -> Unknown (no auto-action)",
			content:  piEntry("assistant", "error"),
			wantStat: StatusUnknown,
			wantKind: PromptNone,
		},
		{
			name:     "assistant aborted -> Unknown",
			content:  piEntry("assistant", "aborted"),
			wantStat: StatusUnknown,
			wantKind: PromptNone,
		},
		{
			name:     "toolResult trailing -> Working (LLM will resume)",
			content:  `{"type":"message","message":{"role":"toolResult"}}`,
			wantStat: StatusWorking,
			wantKind: PromptNone,
		},
		{
			name:     "user trailing -> Working (prompt just sent)",
			content:  `{"type":"message","message":{"role":"user","content":"hi"}}`,
			wantStat: StatusWorking,
			wantKind: PromptNone,
		},
		{
			name:     "non-message entry (session header) -> Unknown",
			content:  `{"type":"session","id":"abc","version":3}`,
			wantStat: StatusUnknown,
			wantKind: PromptNone,
		},
		{
			name:     "non-message entry (model_change) -> Unknown",
			content:  `{"type":"model_change","provider":"<provider>","modelId":"glm-5.2"}`,
			wantStat: StatusUnknown,
			wantKind: PromptNone,
		},
		{
			name:     "invalid JSON -> Unknown",
			content:  `not json at all`,
			wantStat: StatusUnknown,
			wantKind: PromptNone,
		},
		{
			name:     "empty -> Unknown",
			content:  ``,
			wantStat: StatusUnknown,
			wantKind: PromptNone,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, p := PiAdapter{}.Detect(c.content)
			assert.Equal(t, c.wantStat, s, "status mismatch")
			if c.wantKind != PromptNone {
				assert.NotNil(t, p)
				assert.Equal(t, c.wantKind, p.Kind)
			} else {
				assert.Nil(t, p, "no prompt expected")
			}
		})
	}
}

func TestPiAdapter_ReadyPromptHasNoResolve(t *testing.T) {
	// The ready prompt is informational only; there is nothing to auto-dismiss.
	_, p := PiAdapter{}.Detect(piEntry("assistant", "stop"))
	if assert.NotNil(t, p) {
		assert.Nil(t, p.Resolve, "pi ready prompt must not auto-resolve")
	}
}

func TestPiAdapter_SessionArgs(t *testing.T) {
	// SessionArgs implements JournalingAdapter: Pi gets --session-dir pointing
	// at the per-instance directory so the journal observer and Pi agree.
	args := PiAdapter{}.SessionArgs("/tmp/some-dir")
	assert.Equal(t, []string{"--session-dir", "/tmp/some-dir"}, args)
}

func TestPiAdapter_IsJournalingAdapter(t *testing.T) {
	// Compile-time + runtime guarantee: PiAdapter declares the journal-based
	// observation capability. TmuxSession checks for this at Start to decide
	// whether to install a journal observer.
	var a Adapter = PiAdapter{}
	_, ok := a.(JournalingAdapter)
	assert.True(t, ok, "PiAdapter must implement JournalingAdapter")
}

func TestPiAdapter_Registered(t *testing.T) {
	// The pi adapter should be reachable via Lookup once registered in init().
	a := Lookup("/opt/homebrew/bin/pi")
	assert.Equal(t, "pi", a.Name(), "pi adapter should be registered and match")
}
