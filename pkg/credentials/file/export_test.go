package file

// SetCrashHook sets the hook a save calls at each of its steps, for the
// crash tests' child process.
func SetCrashHook(h func(point string)) { crashHook = h }
