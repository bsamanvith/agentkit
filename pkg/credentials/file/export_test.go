package file

// SetCrashHook sets the hook a save calls at each of its steps, for the
// crash tests' child process.
func SetCrashHook(h func(point string)) { crashHook = h }

// SetSyncDir replaces the directory sync of a save until the test ends.
// Tests that call it must not run in parallel.
func SetSyncDir(t interface{ Cleanup(func()) }, f func(dir string) error) {
	old := syncDir
	syncDir = f
	t.Cleanup(func() { syncDir = old })
}
