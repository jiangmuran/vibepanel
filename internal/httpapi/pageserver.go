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
