package httpapi

import (
	"context"

	"github.com/jiangmuran/vibepanel/internal/pages"
	"github.com/jiangmuran/vibepanel/internal/store"
)

// serverTransform is what server.js's transform returned for a snapshot, or
// nil.
func (s *Server) serverTransform(ctx context.Context, page store.SharePage, ns string, m pages.Manifest,
	snap shareSnapshot) any {
	return nil
}

// runServerHook runs one of server.js's hooks for an action.
func (s *Server) runServerHook(ctx context.Context, page store.SharePage, ns string, m pages.Manifest,
	hook, name string, input, visitor map[string]any, action *pages.ActionSpec, by string) (any, error) {
	return nil, dataErrorf("server.js is not available")
}

// runSchedule runs server.js's onSchedule when it is due.
func (s *Server) runSchedule(ctx context.Context, page store.SharePage, ns string, m pages.Manifest) {
}
