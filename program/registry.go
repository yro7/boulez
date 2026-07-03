package program

// registry holds all registered adapters, in registration order. Lookup
// returns the first whose Matches returns true, falling back to NoOpAdapter.
var registry []Adapter

// Register adds an adapter to the registry. Called from package init()
// functions in each adapter file.
func Register(a Adapter) {
	registry = append(registry, a)
}

// Lookup returns the adapter responsible for `program`, or NoOpAdapter if
// none matches. A non-matching agent is silent (no crash, no badge, no
// auto-yes) rather than broken.
func Lookup(program string) Adapter {
	for _, a := range registry {
		if a.Matches(program) {
			return a
		}
	}
	return NoOpAdapter{}
}
