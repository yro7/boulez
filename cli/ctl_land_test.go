package cli

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/yro7/boulez/kernel"
)

// TestCtl_Land_BuildsCorrectRequest verifies `boulez ctl land` constructs the
// correct wire request (method "land", single source branch) WITHOUT going
// to the daemon. It uses the captureHook that `boulez ctl as` already uses to
// intercept rawCtlSession. This is the unit-level pin for the round-trip;
// the end-to-end stdout purity is covered by TestCtl_StdoutIsPureJSON.
func TestCtl_Land_BuildsCorrectRequest(t *testing.T) {
	var captured []kernel.Request
	prev := captureHook
	captureHook = func(reqs []kernel.Request) error {
		captured = reqs
		return nil
	}
	t.Cleanup(func() { captureHook = prev })

	cmd := NewCtlLandCmd()
	cmd.SetArgs([]string{"--target-repo", "/repo", "--target-branch", "main", "--source", "feat-x"})
	require.NoError(t, cmd.Execute())

	require.Len(t, captured, 1, "land sends exactly one request")
	assert.Equal(t, "land", captured[0].Method)

	var p struct {
		TargetRepo   string `json:"target_repo"`
		TargetBranch string `json:"target_branch"`
		Source       string `json:"source"`
	}
	require.NoError(t, json.Unmarshal(captured[0].Params, &p))
	assert.Equal(t, "/repo", p.TargetRepo)
	assert.Equal(t, "main", p.TargetBranch)
	assert.Equal(t, "feat-x", p.Source)
}

// TestCtl_Land_RequiresFlags verifies the required-flag validation.
func TestCtl_Land_RequiresFlags(t *testing.T) {
	cmd := NewCtlLandCmd()
	cmd.SetArgs([]string{"--target-repo", "/repo"}) // missing --source and --target-branch
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "required")
}
