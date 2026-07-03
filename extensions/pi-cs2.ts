/**
 * pi-cs2 — bridge between Pi and Claude Squad (cs2 fork).
 *
 * CONTRACT: This extension and program/pi.go in ~/cs-multirepo share a sentinel
 * string. When Pi finishes a turn and is idle (waiting for user input), this
 * extension injects a visible message containing the sentinel. cs2 captures the
 * tmux pane content and detects the sentinel to show a "Ready" badge and react.
 *
 * The sentinel MUST be kept in sync with `PiReadySentinel` in
 * ~/cs-multirepo/program/pi.go. If you change it here, rebuild cs2.
 *
 * Detection logic (program.PiAdapter.Detect):
 *   - sentinel present in pane content  -> StatusReady  (badge "Ready")
 *   - Pi footer present, no sentinel   -> StatusWorking
 *   - neither                          -> StatusUnknown
 *
 * When the next turn starts, Pi renders new output below the sentinel and it
 * scrolls out of the captured (visible) pane area, so cs2 sees StatusWorking
 * again. The conservative fallback (no sentinel => Working) means the only
 * failure mode is a brief false "Ready" at the very start of a turn, which
 * self-corrects as soon as new content renders.
 */
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";
import { Text } from "@earendil-works/pi-tui";

// MUST match program.PiReadySentinel in ~/cs-multirepo/program/pi.go.
const CS2_READY_SENTINEL = "⟦cs2:ready⟧";

const CS2_CUSTOM_TYPE = "cs2-ready";

export default function (pi: ExtensionAPI) {
	// Render the sentinel as a single subtle dim line so it does not clutter
	// the conversation. The raw sentinel text is what cs2 scans for.
	pi.registerMessageRenderer(CS2_CUSTOM_TYPE, (message, _options, theme) => {
		return new Text(theme.fg("dim", message.content), 0, 0);
	});

	// agent_end fires when the assistant finishes a turn and is idle waiting
	// for the next user input. Emit the sentinel so cs2 can detect "Ready".
	pi.on("agent_end", async (_event, ctx) => {
		pi.sendMessage({
			customType: CS2_CUSTOM_TYPE,
			content: CS2_READY_SENTINEL,
			display: true,
		});
		// Also set the terminal/pane title as a secondary, always-current
		// signal (readable via `tmux display-message -t <pane> -p '#{pane_title}'`).
		// Harmless if cs2 doesn't read it yet; useful for future robustness.
		ctx.ui.setTitle(`${CS2_READY_SENTINEL}`);
	});

	// When a new turn starts, clear the title so the secondary signal does not
	// lag behind (the primary sentinel scrolls off naturally).
	pi.on("agent_start", async (_event, ctx) => {
		ctx.ui.setTitle("pi");
	});
}
