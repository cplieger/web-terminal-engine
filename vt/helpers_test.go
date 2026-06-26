package vt

// didPanic runs fn and reports whether it panicked. Used for boundary checks
// whose only observable difference is an out-of-range access: the unguarded
// path indexes out of range and panics, the guarded path returns cleanly.
func didPanic(fn func()) (panicked bool) {
	defer func() {
		if r := recover(); r != nil {
			panicked = true
		}
	}()
	fn()
	return false
}
