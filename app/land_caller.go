package app

import (
	"encoding/json"
	"fmt"

	"github.com/yro7/boulez/kernel"
	"github.com/yro7/boulez/session"
	"github.com/yro7/boulez/session/git"
)

// socketLandCaller is a session.LandCaller that reaches the kernel over the
// control socket. The TUI does not hold a *kernel.Kernel directly (the kernel
// lives in the daemon process); instead it speaks the wire protocol, exactly
// like `boulez ctl land`. The connection is an unauthenticated (top-level) one,
// which is the only identity Land accepts.
//
// This is the thin adapter that lets session.LandInstance drive the Land
// syscall without the TUI importing or constructing a kernel. It keeps the
// deep-module seam intact: LandInstance knows nothing of transports, and the
// TUI knows nothing of merge internals.
type socketLandCaller struct{}

// newSocketLandCaller returns the production LandCaller backed by the daemon's
// control socket.
func newSocketLandCaller() session.LandCaller {
	return socketLandCaller{}
}

// encodeLandParams builds the wire params for a land request. Kept tiny
// and separate so the LandCaller adapter stays readable.
func encodeLandParams(repoPath, targetBranch, sourceBranch string, strategy git.Strategy) (json.RawMessage, error) {
	return json.Marshal(map[string]interface{}{
		"target_repo":   repoPath,
		"target_branch": targetBranch,
		"source":        sourceBranch,
		"strategy":      int(strategy),
	})
}

// Land sends a `land` request to the kernel and decodes the outcome. The
// wire result is a kernel.LandResult (merge result + host-sync hints); the
// adapter translates it into the seam-level session.LandOutcome so the TUI
// never imports kernel. When the kernel returns a plain git.MergeResult (e.g.
// an older daemon), the host-sync fields degrade to false/"" and the land is
// still reported as successful — graceful degradation, not a hard failure.
func (socketLandCaller) Land(repoPath, targetBranch, sourceBranch string, strategy git.Strategy) (session.LandOutcome, error) {
	socketPath, err := kernel.SocketPath()
	if err != nil {
		return session.LandOutcome{}, fmt.Errorf("land: resolve socket: %w", err)
	}
	params, _ := encodeLandParams(repoPath, targetBranch, sourceBranch, strategy)
	resp, err := kernel.Call(socketPath, kernel.Request{Method: "land", Params: params})
	if err != nil {
		return session.LandOutcome{}, fmt.Errorf("land: call kernel: %w (is the daemon running?)", err)
	}
	if resp.Error != nil {
		return session.LandOutcome{Merge: git.MergeResult{Status: git.MergeConflict, Message: resp.Error.Message}},
			fmt.Errorf("land: %s: %s", resp.Error.Code, resp.Error.Message)
	}

	// Decode the kernel's LandResult. Accept both the structured LandResult
	// (current daemon) and a bare MergeResult (older daemon) so a version skew
	// between TUI and daemon does not break land entirely.
	var out session.LandOutcome
	if err := json.Unmarshal(resp.Result, &out); err != nil {
		return session.LandOutcome{}, fmt.Errorf("land: decode result: %w", err)
	}
	return out, nil
}
