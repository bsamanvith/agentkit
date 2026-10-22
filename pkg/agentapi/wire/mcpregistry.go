package wire

// RegistrySearch is GET /v1/mcp-registry/search: the servers an MCP
// Registry lists for a query that can be added by URL (POST /v1/toolsets).
// An entry says where a server is, not that it is safe: adding one is
// probed and reviewed like any other URL.
type RegistrySearch struct {
	V     int    `json:"v"`
	Query string `json:"query"`
	// Registry is the URL of the registry searched.
	Registry string `json:"registry"`
	// Restricted is true when the caller's tenant limits the URLs admins
	// may add (mcp_servers.allow); each remote's Allowed says whether its
	// URL is one.
	Restricted bool             `json:"restricted"`
	Servers    []RegistryServer `json:"servers"`
}

// RegistryServer is a server of a [RegistrySearch].
type RegistryServer struct {
	// Name is the registry's name ("app.linear/linear").
	Name string `json:"name"`
	// SuggestedName is a toolset name for it, for the add form
	// ("linear").
	SuggestedName string `json:"suggested_name"`
	Title         string `json:"title,omitempty"`
	// Description is the registry's, sanitized: it is third-party text.
	Description   string `json:"description,omitempty"`
	Version       string `json:"version,omitempty"`
	WebsiteURL    string `json:"website_url,omitempty"`
	RepositoryURL string `json:"repository_url,omitempty"`
	// Status is the registry's: "active" or "deprecated".
	Status  string           `json:"status"`
	Remotes []RegistryRemote `json:"remotes"`
}

// RegistryRemote is an endpoint a [RegistryServer] is hosted at.
type RegistryRemote struct {
	// Transport is "http" (Streamable HTTP) or "sse", as
	// [AddToolsetRequest.Transport] takes it.
	Transport string `json:"transport"`
	URL       string `json:"url"`
	// Variables names the "{variables}" in URL to fill in before adding.
	Variables []string `json:"variables,omitempty"`
	// Headers are the headers the server says it takes; a secret one is
	// added as "${NAME}" of a variable the operator allows.
	Headers []RegistryHeader `json:"headers,omitempty"`
	// Allowed is false when the tenant's mcp_servers.allow does not
	// include URL: POST /v1/toolsets refuses it (403).
	Allowed bool `json:"allowed"`
}

// RegistryHeader is a header a [RegistryRemote] takes.
type RegistryHeader struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required"`
	Secret      bool   `json:"secret"`
}
