//go:build !linux

package terminal

// Off Linux the reap domain finds nothing, since no /proc exposes another
// process's environment. The marker is still injected, keeping the spawn path
// identical across builds.

// reapFindByMarker finds nothing off Linux, which makes teardownOnce return
// after its first pass without signalling or logging.
func reapFindByMarker(string) []int { return nil }

// reapAlive reports nothing alive, so a settle window drains immediately.
func reapAlive(int, string) bool { return false }

func reapTerm(int) bool { return false }

func reapKill(int) bool { return false }

func reapResident([]int) uint64 { return 0 }
