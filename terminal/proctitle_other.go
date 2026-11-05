//go:build !linux

package terminal

// Off Linux the probe returns ok=false, which holds the command basename seeded
// at Create; ok=true with nothing found would clear it.

import "os"

func probeForeground(_ *os.File, _ int) autoTitleProbe {
	return autoTitleProbe{}
}
