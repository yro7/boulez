package host

// Lookup resolves a stored host alias to a Host implementation. "local"
// (LocalAlias) returns LocalHost; any other alias returns an SSHHost bound to
// it. Used by FromInstanceData to reconstruct the right Host when restoring
// an instance from storage.
//
// Lookup itself does not validate that the alias is reachable (no ssh
// round-trip); it just constructs the Host. Reachability is exercised lazily
// when the instance starts issuing commands.
func Lookup(alias string) Host {
	if alias == LocalAlias || alias == "" {
		return Local
	}
	return NewSSHHost(alias)
}
