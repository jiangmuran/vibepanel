package httpapi

// pageBackend is the state behind share pages with a backend. One field on
// Server rather than a dozen, so the parts of docs/page-backend.md are found
// in one place and a Server built by hand in a test has all of them zeroed and
// working.
type pageBackend struct {
	secrets secrets
	actions actionDay
	data    pageDataState
	rates   rateBook
	audit   actionAudit
	sources sourcesState
	server  serverState
}
