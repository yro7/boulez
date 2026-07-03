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

func TestPiAdapter_Detect(t *testing.T) {
	cases := []struct {
		name     string
		content  string
		wantStat Status
		wantKind PromptKind
	}{
		{
			name:     "idle footer with cs2 sentinel -> Ready",
			content:  "0.0%/1.0M (auto)                                        <provider> <model> • high\n" + PiReadySentinel,
			wantStat: StatusReady,
			wantKind: PromptReady,
		},
		{
			name:     "working footer, no sentinel -> Working (conservative)",
			content:  "↑7.0k ↓63 0.4%/1.0M (auto)                              <provider> <model> • high",
			wantStat: StatusWorking,
			wantKind: PromptNone,
		},
		{
			name:     "idle footer, no sentinel -> Working (conservative)",
			content:  "0.0%/1.0M (auto)                                        <provider> <model> • high",
			wantStat: StatusWorking,
			wantKind: PromptNone,
		},
		{
			name:     "not a pi pane",
			content:  "random claude output here",
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

func TestPiReadySentinelIsStable(t *testing.T) {
	// Guard against accidental edits to the sentinel string: it is a shared
	// contract with the pi-cs2 Pi extension. If this test breaks, update both
	// sides (program/pi.go and extensions/pi-cs2.ts) together.
	assert.Equal(t, "⟦cs2:ready⟧", PiReadySentinel)
}

func TestPiAdapter_Registered(t *testing.T) {
	// The pi adapter should be reachable via Lookup once registered in init().
	a := Lookup("/opt/homebrew/bin/pi")
	assert.Equal(t, "pi", a.Name(), "pi adapter should be registered and match")
}
