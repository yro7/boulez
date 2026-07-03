package program

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGeminiAdapter_NameAndMatch(t *testing.T) {
	a := GeminiAdapter{}
	assert.Equal(t, "gemini", a.Name())
	assert.True(t, a.Matches("gemini"))
	assert.True(t, a.Matches("gemini --model x"))
	assert.False(t, a.Matches("claude"))
}

func TestGeminiAdapter_Detect(t *testing.T) {
	s, p := GeminiAdapter{}.Detect("Run command? Yes, allow once")
	assert.Equal(t, StatusReady, s)
	assert.NotNil(t, p)
	assert.Equal(t, PromptReady, p.Kind)

	s, p = GeminiAdapter{}.Detect("working")
	assert.Equal(t, StatusWorking, s)
	assert.Nil(t, p)
}
