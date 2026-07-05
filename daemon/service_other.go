//go:build !darwin && !linux

package daemon

// On platforms without launchd or systemd (e.g. Windows, *BSD), install is
// not supported — the dev fallback (C2.5) is `nohup boulez daemon run &`. No
// custom supervisor is built.

func serviceManager() string { return "" }

func uidImpl() (string, error) { return "", ErrServiceUnsupported{} }

func install() error { return ErrServiceUnsupported{} }

func uninstall() error { return nil }
