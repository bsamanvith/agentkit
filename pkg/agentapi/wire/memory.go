package wire

// MemoryView is GET /v1/agents/{name}/memory: one path of the caller's
// memory of an agent, a file's text or a directory's listing, with what the
// whole memory uses against its limits. Memory is plain readable text.
type MemoryView struct {
	V     int    `json:"v"`
	Agent string `json:"agent"`
	// Scope is ScopeUser (the caller's own memory) or ScopeTenant (shared
	// by everyone of the tenant).
	Scope string `json:"scope"`
	Path  string `json:"path"`
	// Dir says Path is a directory: Entries lists it, two levels deep.
	// Otherwise Text is the file's.
	Dir     bool          `json:"dir"`
	Size    int64         `json:"size"`
	Text    string        `json:"text,omitempty"`
	Entries []MemoryEntry `json:"entries,omitempty"`
	Usage   MemoryUsage   `json:"usage"`
	Limits  MemoryLimits  `json:"limits"`
}

// MemoryEntry is a file or directory of a listing; a directory's size is
// its files'.
type MemoryEntry struct {
	Path string `json:"path"`
	Dir  bool   `json:"dir"`
	Size int64  `json:"size"`
}

// MemoryUsage is what the whole memory holds.
type MemoryUsage struct {
	Bytes int64 `json:"bytes"`
	Files int   `json:"files"`
}

// MemoryLimits are the agent's memory caps: one file's bytes, all files'
// bytes, and how many files.
type MemoryLimits struct {
	FileBytes  int64 `json:"file_bytes"`
	ScopeBytes int64 `json:"scope_bytes"`
	Files      int   `json:"files"`
}
