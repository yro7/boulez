package program

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestClaudeAdapter_NameAndMatch(t *testing.T) {
	a := ClaudeAdapter{}
	assert.Equal(t, "claude", a.Name())
	assert.True(t, a.Matches("claude"))
	assert.True(t, a.Matches("/usr/local/bin/claude"))
	assert.False(t, a.Matches("aider"))
}

func TestClaudeAdapter_Detect(t *testing.T) {
	cases := []struct {
		name      string
		content   string
		wantStat  Status
		wantPrompt bool
		wantKind  PromptKind
	}{
		{
			name:      "trust folder prompt",
			content:   "Do you trust the files in this folder?",
			wantStat:  StatusPermission,
			wantPrompt: true,
			wantKind:  PromptTrust,
		},
		{
			name:      "new MCP server prompt",
			content:   "Detected new MCP server from config",
			wantStat:  StatusPermission,
			wantPrompt: true,
			wantKind:  PromptTrust,
		},
		{
			name:      "ready for input",
			content:   "No, and tell Claude what to do differently",
			wantStat:  StatusReady,
			wantPrompt: true,
			wantKind:  PromptReady,
		},
		{
			name:      "working",
			content:   "random pane content while agent runs",
			wantStat:  StatusWorking,
			wantPrompt: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, p := ClaudeAdapter{}.Detect(c.content)
			assert.Equal(t, c.wantStat, s)
			if c.wantPrompt {
				assert.NotNil(t, p)
				assert.Equal(t, c.wantKind, p.Kind)
				if p.Resolve != nil {
					// Resolvable prompt should not error against a stub responder.
					assert.NoError(t, p.Resolve(stubResponder{}))
				}
			} else {
				assert.Nil(t, p)
			}
		})
	}
}

type stubResponder struct{}

func (stubResponder) TapEnter() error          { return nil }
func (stubResponder) TapDAndEnter() error      { return nil }
func (stubResponder) SendKeys(string) error    { return nil }
