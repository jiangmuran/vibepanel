package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jiangmuran/vibepanel/internal/pages"
	"github.com/jiangmuran/vibepanel/internal/store"
)

// A share page's data: docs/page-backend.md §2. Every writer -- the settings
// routes, an admin page, server.js, a visitor action -- goes through
// applyPageData, so the rules are checked in one place.

// pageDataState serialises writes and caches reads.
//
// One mutex for every page's writes, because an increment and an append are a
// read and a write of the same row, and two of them interleaved lose one. The
// traffic is people and kiosks, not a hot path. Reads are cached per page and
// namespace until a write bumps the revision, so a wall polling every two
// seconds is not a query per poll.
type pageDataState struct {
	writeMu sync.Mutex
	mu      sync.Mutex
	rev     map[string]uint64
	cache   map[string]cachedPageData
}

type cachedPageData struct {
	rev  uint64
	at   time.Time
	rows map[string]store.PageDatum
}

// pageDataCacheTTL bounds how stale a cached read may be. Writes through this
// process bump the revision and show on the next poll; this is for the ones
// that do not -- `vibepanel page data set`, an import from the CLI.
const pageDataCacheTTL = 5 * time.Second

func dataCacheKey(pageID, ns string) string { return pageID + "|" + ns }

// pageDataRevision is the current revision of a page's namespace, for memo keys.
func (s *Server) pageDataRevision(pageID, ns string) uint64 {
	st := &s.pb.data
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.rev[dataCacheKey(pageID, ns)]
}

func (s *Server) bumpPageData(pageID, ns string) {
	st := &s.pb.data
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.rev == nil {
		st.rev = map[string]uint64{}
	}
	st.rev[dataCacheKey(pageID, ns)]++
}

func (s *Server) pageDataRows(ctx context.Context, pageID, ns string) (map[string]store.PageDatum, error) {
	st := &s.pb.data
	key := dataCacheKey(pageID, ns)
	st.mu.Lock()
	rev := st.rev[key]
	if c, ok := st.cache[key]; ok && c.rev == rev && time.Since(c.at) < pageDataCacheTTL {
		st.mu.Unlock()
		return c.rows, nil
	}
	st.mu.Unlock()
	rows, err := s.DB.PageData(ctx, pageID, ns)
	if err != nil {
		return nil, err
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.cache == nil {
		st.cache = map[string]cachedPageData{}
	}
	if len(st.cache) > 512 {
		st.cache = map[string]cachedPageData{}
	}
	if st.rev[key] == rev {
		st.cache[key] = cachedPageData{rev: rev, at: time.Now(), rows: rows}
	}
	return rows, nil
}

// resolvePageData is every declared key with its stored value, or its zero
// when nothing is stored or what is stored no longer fits the manifest. With
// adminToo false, keys marked "admin" are left out.
func resolvePageData(m pages.Manifest, rows map[string]store.PageDatum, adminToo bool) (map[string]any, map[string]int64) {
	values := map[string]any{}
	updated := map[string]int64{}
	for key, spec := range m.Data {
		if spec.Visibility == pages.VisibilityAdmin && !adminToo {
			continue
		}
		values[key] = spec.Zero()
		row, ok := rows[key]
		if !ok {
			continue
		}
		var v any
		if json.Unmarshal(row.Value, &v) != nil {
			continue
		}
		if clean, err := spec.Check(v, false); err == nil {
			values[key] = clean
			updated[key] = row.UpdatedAt
		}
	}
	return values, updated
}

// pageDataOp is one change to one key.
type pageDataOp struct {
	Kind  string // "set", "increment", "append", "reset"
	Key   string
	Value any            // set
	By    float64        // increment
	Item  map[string]any // append: the item's fields
}

// dataError is a change the rules refuse, as opposed to a store failure: the
// caller answers 400 with it.
type dataError struct{ msg string }

func (e dataError) Error() string { return e.msg }

func dataErrorf(format string, args ...any) error { return dataError{fmt.Sprintf(format, args...)} }

// applyPageData checks and applies changes to one namespace, all or none,
// and returns every changed key's new value.
//
// visitor holds text to the visitor's stricter reading. allowSetAny lets
// server code set a counter (to a whole number); nobody may set a log.
func (s *Server) applyPageData(ctx context.Context, pageID, ns string, m pages.Manifest, ops []pageDataOp,
	by string, visitor, allowSetAny bool) (map[string]any, error) {
	st := &s.pb.data
	st.writeMu.Lock()
	defer st.writeMu.Unlock()

	rows, err := s.DB.PageData(ctx, pageID, ns)
	if err != nil {
		return nil, err
	}
	current, _ := resolvePageData(m, rows, true)
	changed := map[string]any{}
	writes := map[string][]byte{}
	resets := map[string]bool{}
	for _, op := range ops {
		spec := m.Data[op.Key]
		if spec == nil {
			return nil, dataErrorf("this page declares no data %q", op.Key)
		}
		var next any
		switch op.Kind {
		case "set":
			if spec.Type == pages.DataLog || spec.Type == pages.DataCounter && !allowSetAny {
				return nil, dataErrorf("%s is a %s; it changes by %s, not by setting it", op.Key, spec.Type,
					map[string]string{pages.DataLog: "append", pages.DataCounter: "increment"}[spec.Type])
			}
			clean, cerr := spec.Check(op.Value, visitor)
			if cerr != nil {
				return nil, dataErrorf("%s %s", op.Key, cerr.Error())
			}
			next = clean
		case "increment":
			if spec.Type != pages.DataCounter {
				return nil, dataErrorf("%s is not a counter", op.Key)
			}
			if op.By != math.Trunc(op.By) || math.Abs(op.By) > 1e9 {
				return nil, dataErrorf("increment by a whole number")
			}
			n := current[op.Key].(float64) + op.By
			if n < 0 {
				n = 0
			}
			next = n
		case "append":
			if spec.Type != pages.DataLog {
				return nil, dataErrorf("%s is not a log", op.Key)
			}
			fields, cerr := pages.CheckInput(pages.ItemFields(spec.Item), op.Item, visitor)
			if cerr != nil {
				return nil, dataErrorf("%s item %s", op.Key, cerr.Error())
			}
			entries := append([]any{}, current[op.Key].([]any)...)
			entries = append(entries, pages.NewLogEntry(fields, time.Now().Unix()))
			if limit := spec.LogLimit(); len(entries) > limit {
				entries = entries[len(entries)-limit:]
			}
			next = entries
		case "reset":
			next = spec.Zero()
			resets[op.Key] = true
			delete(writes, op.Key)
			current[op.Key] = next
			changed[op.Key] = next
			continue
		default:
			return nil, dataErrorf("unknown change %q", op.Kind)
		}
		raw, jerr := json.Marshal(next)
		if jerr != nil {
			return nil, dataErrorf("%s is not JSON", op.Key)
		}
		writes[op.Key] = raw
		delete(resets, op.Key)
		current[op.Key] = next
		changed[op.Key] = next
	}
	batch := make([]store.PageDataWrite, 0, len(writes)+len(resets))
	for k, raw := range writes {
		batch = append(batch, store.PageDataWrite{Key: k, Value: raw})
	}
	for k := range resets {
		batch = append(batch, store.PageDataWrite{Key: k})
	}
	if len(batch) == 0 {
		return changed, nil
	}
	err = s.DB.WritePageData(ctx, pageID, ns, batch, by, pages.MaxDataBytes, pages.MaxDataKeys)
	switch {
	case errors.Is(err, store.ErrPageDataTooLarge):
		return nil, dataErrorf("this page's data would be larger than %d KiB", pages.MaxDataBytes>>10)
	case errors.Is(err, store.ErrPageDataTooManyKeys):
		return nil, dataErrorf("this page's data would have more than %d keys", pages.MaxDataKeys)
	case err != nil:
		return nil, err
	}
	s.bumpPageData(pageID, ns)
	return changed, nil
}

// ─── which manifest a namespace answers to ────────────────────────────────

var errNotPublished = errors.New("this page has not been published; its live data has no schema yet")

// pageManifestFor is the manifest a namespace's data is checked against: the
// published version's for live, the draft directory's for draft.
func (s *Server) pageManifestFor(ctx context.Context, page store.SharePage, ns string) (pages.Manifest, error) {
	if ns == store.PageDataDraft {
		return draftManifest(page.SourceDir)
	}
	return publishedManifest(ctx, s.DB, page)
}

// publishedManifest is the manifest of a page's published version.
func publishedManifest(ctx context.Context, db *store.DB, page store.SharePage) (pages.Manifest, error) {
	if page.PublishedVersion == 0 {
		return pages.Manifest{}, errNotPublished
	}
	v, err := db.SharePageVersionByNumber(ctx, page.ID, page.PublishedVersion)
	if err != nil {
		return pages.Manifest{}, err
	}
	return pages.DecodeStored(v.Manifest), nil
}

// ─── the settings routes ──────────────────────────────────────────────────

func (s *Server) registerPageDataRoutes(r chi.Router) {
	r.Get("/settings/pages/{pageID}/data", s.handleGetPageData)
	r.Delete("/settings/pages/{pageID}/data", s.handleClearPageData)
	r.Put("/settings/pages/{pageID}/data/{key}", s.handlePageDataOp("set"))
	r.Post("/settings/pages/{pageID}/data/{key}/increment", s.handlePageDataOp("increment"))
	r.Post("/settings/pages/{pageID}/data/{key}/append", s.handlePageDataOp("append"))
	r.Delete("/settings/pages/{pageID}/data/{key}", s.handlePageDataOp("reset"))
}

type pageDataResponse struct {
	Schema    map[string]*pages.DataSpec `json:"schema"`
	Values    map[string]any             `json:"values"`
	UpdatedAt map[string]int64           `json:"updatedAt"`
	Bytes     int                        `json:"bytes"`
	Limit     int                        `json:"limit"`
}

// pageDataTarget reads the page, namespace and manifest a data route is for,
// answering the request itself when it cannot.
func (s *Server) pageDataTarget(w http.ResponseWriter, r *http.Request) (store.SharePage, string, pages.Manifest, bool) {
	ctx := r.Context()
	page, err := s.DB.SharePageByID(ctx, chi.URLParam(r, "pageID"))
	if err != nil {
		s.writeStoreErr(w, err)
		return store.SharePage{}, "", pages.Manifest{}, false
	}
	ns := r.URL.Query().Get("ns")
	if ns == "" {
		ns = store.PageDataLive
	}
	if !store.ValidPageDataNamespace(ns) {
		writeErr(w, http.StatusBadRequest, `ns is "live" or "draft"`)
		return store.SharePage{}, "", pages.Manifest{}, false
	}
	m, err := s.pageManifestFor(ctx, page, ns)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.writeStoreErr(w, err)
		} else {
			writeErr(w, http.StatusConflict, err.Error())
		}
		return store.SharePage{}, "", pages.Manifest{}, false
	}
	return page, ns, m, true
}

func (s *Server) pageDataBody(ctx context.Context, page store.SharePage, ns string, m pages.Manifest) (pageDataResponse, error) {
	rows, err := s.pageDataRows(ctx, page.ID, ns)
	if err != nil {
		return pageDataResponse{}, err
	}
	values, updated := resolvePageData(m, rows, true)
	bytes := 0
	for k, row := range rows {
		bytes += len(row.Value) + len(k)
	}
	schema := m.Data
	if schema == nil {
		schema = map[string]*pages.DataSpec{}
	}
	return pageDataResponse{Schema: schema, Values: values, UpdatedAt: updated, Bytes: bytes, Limit: pages.MaxDataBytes}, nil
}

func (s *Server) handleGetPageData(w http.ResponseWriter, r *http.Request) {
	page, ns, m, ok := s.pageDataTarget(w, r)
	if !ok {
		return
	}
	body, err := s.pageDataBody(r.Context(), page, ns, m)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, body)
}

func (s *Server) handleClearPageData(w http.ResponseWriter, r *http.Request) {
	page, ns, _, ok := s.pageDataTarget(w, r)
	if !ok {
		return
	}
	s.pb.data.writeMu.Lock()
	err := s.DB.ClearPageData(r.Context(), page.ID, ns)
	s.pb.data.writeMu.Unlock()
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	s.bumpPageData(page.ID, ns)
	if u, ok := currentUserFrom(r); ok {
		s.audit(r.Context(), "page.data_changed", u.Username, s.clientIP(r), page.Name+" "+ns+": cleared")
	}
	w.WriteHeader(http.StatusNoContent)
}

// handlePageDataOp is one change to one key from the settings page.
func (s *Server) handlePageDataOp(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		page, ns, m, ok := s.pageDataTarget(w, r)
		if !ok {
			return
		}
		op := pageDataOp{Kind: kind, Key: chi.URLParam(r, "key")}
		if kind != "reset" {
			var req struct {
				Value any            `json:"value"`
				By    *float64       `json:"by"`
				Item  map[string]any `json:"item"`
			}
			if !decode(w, r, &req) {
				return
			}
			op.Value, op.Item = req.Value, req.Item
			if kind == "increment" {
				op.By = 1
				if req.By != nil {
					op.By = *req.By
				}
			}
			if kind == "append" && req.Item == nil {
				writeErr(w, http.StatusBadRequest, `append takes {"item": {...}}`)
				return
			}
		}
		u, _ := currentUserFrom(r)
		s.writePageDataOp(w, r, page, ns, m, op, u.Username, u.Username)
	}
}

// writePageDataOp applies one owner or admin change and answers it.
func (s *Server) writePageDataOp(w http.ResponseWriter, r *http.Request, page store.SharePage, ns string,
	m pages.Manifest, op pageDataOp, by, auditUser string) {
	changed, err := s.applyPageData(r.Context(), page.ID, ns, m, []pageDataOp{op}, by, false, false)
	var de dataError
	if errors.As(err, &de) {
		writeErr(w, http.StatusBadRequest, de.msg)
		return
	}
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	s.audit(r.Context(), "page.data_changed", auditUser, s.clientIP(r),
		page.Name+" "+ns+": "+op.Kind+" "+op.Key)
	if op.Kind == "reset" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"value": changed[op.Key]})
}

// exportPageData is a page's live values as the zip's vibepanel-data.json.
func exportPageData(ctx context.Context, db *store.DB, page store.SharePage) ([]byte, error) {
	m, err := publishedManifest(ctx, db, page)
	if err != nil {
		return nil, err
	}
	rows, err := db.PageData(ctx, page.ID, store.PageDataLive)
	if err != nil {
		return nil, err
	}
	values, _ := resolvePageData(m, rows, true)
	return json.MarshalIndent(values, "", "  ")
}

// importPageData writes an archive's vibepanel-data.json into a namespace, key
// by key, keeping what fits the manifest and reporting what does not.
func importPageData(ctx context.Context, db *store.DB, pageID, ns string, m pages.Manifest, raw []byte, by string) []string {
	var in map[string]any
	if json.Unmarshal(raw, &in) != nil {
		return []string{"vibepanel-data.json is not a JSON object"}
	}
	var skipped []string
	var ops []pageDataOp
	for key, v := range in {
		spec := m.Data[key]
		if spec == nil {
			skipped = append(skipped, key+": not declared")
			continue
		}
		clean, err := spec.Check(v, false)
		if err != nil {
			skipped = append(skipped, key+": "+err.Error())
			continue
		}
		ops = append(ops, pageDataOp{Kind: "raw", Key: key, Value: clean})
	}
	var batch []store.PageDataWrite
	for _, op := range ops {
		b, err := json.Marshal(op.Value)
		if err == nil {
			batch = append(batch, store.PageDataWrite{Key: op.Key, Value: b})
		}
	}
	if len(batch) > 0 {
		if err := db.WritePageData(ctx, pageID, ns, batch, by, pages.MaxDataBytes, pages.MaxDataKeys); err != nil {
			skipped = append(skipped, "data: "+err.Error())
		}
	}
	return skipped
}
