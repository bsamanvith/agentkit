package wsapi

// OnSlow has f called each time the server finds a client too slow (after
// its streams got their gaps), so a test knows when to let the client read.
func OnSlow(s *Server, f func()) { s.slow = f }

// CSRFHeader is the header the binding puts a hello's CSRF token in.
const CSRFHeader = csrfHeader
