package httpapi

import (
	"net/url"

	"github.com/jiangmuran/vibepanel/internal/pages"
)

// sourceHost is the host a source's URL reaches, with its port when it is
// not 443, which is what an owner approves.
func sourceHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if p := u.Port(); p != "" && p != "443" {
		return u.Hostname() + ":" + p
	}
	return u.Hostname()
}

// sourceResult is one source's last fetch, as a page sees it.
type sourceResult struct {
	OK        bool   `json:"ok"`
	FetchedAt int64  `json:"fetchedAt"`
	Status    int    `json:"status"`
	Error     string `json:"error"`
	Value     any    `json:"value"`
}

// sourceResults is every declared source's last result.
func (s *Server) sourceResults(pageID string, m pages.Manifest) map[string]*sourceResult {
	out := map[string]*sourceResult{}
	for _, src := range m.Sources {
		out[src.Key] = &sourceResult{Error: "not fetched yet"}
	}
	return out
}

// markWatched records that something is looking at a page, which is what
// keeps its sources and schedule running.
func (s *Server) markWatched(pageID string) {}
