package program

// init registers the built-in adapters. Importing the program package (which
// session/tmux does) triggers this automatically, so the registry is populated
// before any Lookup call. Adding a new agent = add its file + a Register line
// here; no other package needs to change.
func init() {
	Register(ClaudeAdapter{})
	Register(AiderAdapter{})
	Register(GeminiAdapter{})
}
