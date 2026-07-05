/**
 * pi-boulez — bridge between Pi and Boulez.
 *
 * CONTRACT: This extension and program/pi.go in the boulez repo share a sentinel
 * string. When Pi finishes a turn and is idle (waiting for user input), this
 * extension renders the sentinel as a widget line in the pane. Boulez captures the
 * tmux pane content (`tmux capture-pane -p`) and detects the sentinel to show a
 * "Ready" badge and react.
 *
 * The sentinel MUST be kept in sync with `PiReadySentinel` in
 * program/pi.go. If you change it here, rebuild boulez.
 *
 * Detection logic (program.PiAdapter.Detect):
 *   - sentinel present in pane content  -> StatusReady  (badge "Ready")
 *   - Pi footer present, no sentinel   -> StatusWorking
 *   - neither                          -> StatusUnknown
 *
 * IMPLEMENTATION NOTE — why a widget, not sendMessage:
 * `pi.sendMessage({ display: true })` defaults to `deliverAs: "steer"`, which
 * when the agent is idle (as it is at `agent_end`) is delivered immediately and
 * triggers a new LLM turn. That re-fires `agent_end`, which re-emits the
 * sentinel → infinite loop (the agent answers its own sentinel forever).
 * `ctx.ui.setWidget` is pure UI: it renders into the pane body (so
 * `tmux capture-pane -p` sees it) but is never sent to the LLM and never
 * triggers a turn. We clear it on `agent_start` so it disappears the moment a
 * new turn begins (no reliance on scroll-off).
 */
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";

// MUST match program.PiReadySentinel in program/pi.go.
const BOULEZ_READY_SENTINEL = "⟦boulez:ready⟧";

// Widget key under which the sentinel is rendered. Stable so agent_start can
// clear exactly what agent_end set.
const BOULEZ_WIDGET_KEY = "boulez-ready";

export default function (pi: ExtensionAPI) {
	// On agent_end: render the sentinel as a dim widget line below the editor.
	// Pure UI — never reaches the LLM, never triggers a turn.
	pi.on("agent_end", async (_event, ctx) => {
		ctx.ui.setWidget(
			BOULEZ_WIDGET_KEY,
			(_tui, theme) => ({
				render: () => [theme.fg("dim", BOULEZ_READY_SENTINEL)],
				invalidate: () => {},
			}),
			{ placement: "belowEditor" },
		);

		// Secondary, always-current signal readable via
		// `tmux display-message -t <pane> -p '#{pane_title}'`. Harmless if boulez
		// doesn't read it yet; useful for future robustness.
		ctx.ui.setTitle(BOULEZ_READY_SENTINEL);
	});

	// On agent_start: clear the widget so the sentinel disappears the moment a
	// new turn begins (boulez then sees StatusWorking again).
	pi.on("agent_start", async (_event, ctx) => {
		ctx.ui.setWidget(BOULEZ_WIDGET_KEY, undefined);
		ctx.ui.setTitle("pi");
	});
}
