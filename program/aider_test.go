package program

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAiderAdapter_NameAndMatch(t *testing.T) {
	a := AiderAdapter{}
	assert.Equal(t, "aider", a.Name())
	assert.True(t, a.Matches("aider"))
	assert.True(t, a.Matches("aider --model ollama_chat/gemma3:1b"))
	assert.False(t, a.Matches("claude"))
}

func TestAiderAdapter_Detect(t *testing.T) {
	s, p := AiderAdapter{}.Detect("Edit the files? (Y)es/(N)o/(D)on't ask again")
	assert.Equal(t, StatusReady, s)
	assert.NotNil(t, p)
	assert.Equal(t, PromptReady, p.Kind)

	s, p = AiderAdapter{}.Detect("working on it")
	assert.Equal(t, StatusWorking, s)
	assert.Nil(t, p)
}
