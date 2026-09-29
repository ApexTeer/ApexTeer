//go:build !linux

package bench

// freeSpace cannot answer off Linux, and says so rather than guessing. The disk benchmark
// then relies on the write itself failing, which is a clear error too, just later.
func freeSpace(_ string) (int64, bool) {
	return 0, false
}

// availableMemory cannot answer off Linux either, so the memory test keeps the size it was
// asked for: a machine without /proc is the development machine, where the default fits.
func availableMemory() (int64, bool) {
	return 0, false
}
