// The items API: the composer's write, the home grid's read, the item page's
// edit, and the Recently deleted view. It is the in-app save path from
// design.md §5 — it writes the local database and enters the same enrichment
// queue as a remote save, without a round trip through the Worker.
package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/AutumnsGrove/Harbor/server/internal/canon"
	"github.com/AutumnsGrove/Harbor/server/internal/model"
	"github.com/AutumnsGrove/Harbor/server/internal/store"
)

const (
	// maxItemBodyBytes caps an item request. A save is a URL, a title and a
	// note; a megabyte is generous and still finite (invariant 7).
	maxItemBodyBytes = 1 << 20

	defaultItemsLimit = 50
	maxItemsLimit     = 200
)

// itemDTO is the wire shape. It is not model.Item: the row has enrichment
// columns the UI does not read yet, and the JSON names are camelCase for the
// SvelteKit client. Adding a field here is a frontend contract change too.
type itemDTO struct {
	ID          string           `json:"id"`
	URL         *string          `json:"url"`
	URLOriginal string           `json:"urlOriginal"`
	Title       string           `json:"title"`
	Note        string           `json:"note"`
	Summary     string           `json:"summary"`
	Kind        model.Kind       `json:"kind"`
	KindSource  model.KindSource `json:"kindSource"`
	Source      model.Source     `json:"source"`
	State       model.State      `json:"state"`
	Attempts    int              `json:"attempts,omitempty"`
	LastError   string           `json:"lastError,omitempty"`

	// Filled for one item, left empty in a list so the grid is one query.
	Tags      []string `json:"tags,omitempty"`
	ReadLater bool     `json:"readLater,omitempty"`

	CreatedAt time.Time  `json:"createdAt"`
	SavedAt   time.Time  `json:"savedAt"`
	UpdatedAt time.Time  `json:"updatedAt"`
	DeletedAt *time.Time `json:"deletedAt,omitempty"`
}

type itemListResponse struct {
	Items []itemDTO `json:"items"`
	Next  *string   `json:"next"`
}

type itemResponse struct {
	Item itemDTO `json:"item"`
}

type saveResponse struct {
	Item   itemDTO `json:"item"`
	Merged bool    `json:"merged"`
}

type lookupResponse struct {
	Exists bool     `json:"exists"`
	Item   *itemDTO `json:"item,omitempty"`
}

func toDTO(item model.Item) itemDTO {
	return itemDTO{
		ID:          item.ID,
		URL:         item.URL,
		URLOriginal: item.URLOriginal,
		Title:       item.Title,
		Note:        item.Note,
		Summary:     item.Summary,
		Kind:        item.Kind,
		KindSource:  item.KindSource,
		Source:      item.Source,
		State:       item.State,
		Attempts:    item.Attempts,
		LastError:   item.LastError,
		CreatedAt:   item.CreatedAt,
		SavedAt:     item.SavedAt,
		UpdatedAt:   item.UpdatedAt,
		DeletedAt:   item.DeletedAt,
	}
}

// requireLibrary answers 503 rather than panicking when the binary was built
// without a database (the phase-0 tests) or the library is closed.
func (s *Server) requireLibrary(w http.ResponseWriter) bool {
	if s.library == nil {
		writeError(w, http.StatusServiceUnavailable, "the library is not open")
		return false
	}
	return true
}

// --- /api/items ------------------------------------------------------------

func (s *Server) items(w http.ResponseWriter, r *http.Request) {
	if !s.requireLibrary(w) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.listItems(w, r)
	case http.MethodPost:
		s.createItem(w, r)
	default:
		w.Header().Set("Allow", "GET, POST")
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// createItem is the in-app save: canonicalise, merge if the URL exists, and
// hand the row to the enrichment queue (which is the state column, not a call).
func (s *Server) createItem(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL       *string      `json:"url"`
		Note      string       `json:"note"`
		Title     string       `json:"title"`
		ReadLater bool         `json:"readLater"`
		Source    model.Source `json:"source"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}

	source := req.Source
	if source == "" {
		source = model.SourceApp
	}
	switch source {
	case model.SourceApp, model.SourceQuick, model.SourceShare:
	default:
		// Telegram, shortcut and import saves come through their own paths and
		// must not be claimable by a request from the app.
		writeError(w, http.StatusBadRequest, fmt.Sprintf("source %q cannot save through this route", source))
		return
	}

	in := store.ItemInput{
		ID:        uuid.NewString(),
		Title:     strings.TrimSpace(req.Title),
		Note:      req.Note,
		ReadLater: req.ReadLater,
		Source:    source,
		Priority:  0,
	}

	if req.URL != nil && strings.TrimSpace(*req.URL) != "" {
		raw := strings.TrimSpace(*req.URL)
		canonical, err := canon.CanonicalURL(raw)
		if err != nil {
			writeError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		in.URL = &canonical
		in.URLOriginal = raw
		in.Kind = model.KindArticle
		if in.Title == "" {
			in.Title = canonical
		}
	} else if strings.TrimSpace(req.Note) == "" {
		writeError(w, http.StatusBadRequest, "a save needs a url or a note")
		return
	} else {
		in.Kind = model.KindNote
	}

	item, merged, err := s.library.MergeItem(r.Context(), in)
	if err != nil {
		s.log.Error("saving an item", "error", err)
		writeError(w, http.StatusInternalServerError, "could not save the item")
		return
	}
	writeJSON(w, http.StatusCreated, saveResponse{Item: s.detail(r, item), Merged: merged})
}

func (s *Server) listItems(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, err := parseItemsLimit(q.Get("limit"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	opts := store.ListOptions{Limit: limit, OnlyDeleted: q.Get("deleted") == "true"}
	if raw := q.Get("cursor"); raw != "" {
		cursor, err := decodeCursor(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "cursor is not one this server issued")
			return
		}
		opts.After = &cursor
	}

	items, err := s.library.ListItems(r.Context(), opts)
	if err != nil {
		s.log.Error("listing items", "error", err)
		writeError(w, http.StatusInternalServerError, "could not read the library")
		return
	}

	response := itemListResponse{Items: make([]itemDTO, 0, len(items))}
	for _, item := range items {
		response.Items = append(response.Items, toDTO(item))
	}
	if len(items) == limit {
		last := items[len(items)-1]
		next := encodeCursor(store.Cursor{SavedAt: last.SavedAt, Seq: last.Seq})
		response.Next = &next
	}
	writeJSON(w, http.StatusOK, response)
}

// --- /api/items/lookup -----------------------------------------------------

// lookupItem answers the composer's merge warning. The rule lives on the
// server, never in the UI (plans/02 risks: merge drift).
func (s *Server) lookupItem(w http.ResponseWriter, r *http.Request) {
	if !s.requireLibrary(w) {
		return
	}
	raw := strings.TrimSpace(r.URL.Query().Get("url"))
	if raw == "" {
		writeError(w, http.StatusBadRequest, "url is required")
		return
	}
	canonical, err := canon.CanonicalURL(raw)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	item, err := s.library.GetItemByURL(r.Context(), canonical)
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeJSON(w, http.StatusOK, lookupResponse{Exists: false})
	case err != nil:
		s.log.Error("looking up an item", "error", err)
		writeError(w, http.StatusInternalServerError, "could not look up the item")
	default:
		dto := s.detail(r, item)
		writeJSON(w, http.StatusOK, lookupResponse{Exists: true, Item: &dto})
	}
}

// --- /api/items/{id} -------------------------------------------------------

func (s *Server) itemByPath(w http.ResponseWriter, r *http.Request) {
	if !s.requireLibrary(w) {
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/items/")
	id, action, _ := strings.Cut(rest, "/")
	if id == "" || strings.Contains(id, "/") {
		writeError(w, http.StatusNotFound, "not found")
		return
	}

	switch action {
	case "":
		switch r.Method {
		case http.MethodGet:
			s.getItem(w, r, id)
		case http.MethodPatch:
			s.updateItem(w, r, id)
		case http.MethodDelete:
			s.deleteItem(w, r, id)
		default:
			w.Header().Set("Allow", "GET, PATCH, DELETE")
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	case "restore", "retry":
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if action == "restore" {
			s.restoreItem(w, r, id)
			return
		}
		s.retryItem(w, r, id)
	default:
		writeError(w, http.StatusNotFound, "not found")
	}
}

func (s *Server) getItem(w http.ResponseWriter, r *http.Request, id string) {
	item, err := s.library.GetItem(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "no such item")
		return
	}
	if err != nil {
		s.log.Error("reading an item", "error", err, "id", id)
		writeError(w, http.StatusInternalServerError, "could not read the item")
		return
	}
	writeJSON(w, http.StatusOK, itemResponse{Item: s.detail(r, item)})
}

func (s *Server) updateItem(w http.ResponseWriter, r *http.Request, id string) {
	var req struct {
		Title   *string     `json:"title"`
		Note    *string     `json:"note"`
		Summary *string     `json:"summary"`
		Kind    *model.Kind `json:"kind"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	patch := store.ItemPatch{Title: req.Title, Note: req.Note, Summary: req.Summary, Kind: req.Kind}
	if patch == (store.ItemPatch{}) {
		writeError(w, http.StatusBadRequest, "nothing to change")
		return
	}

	item, err := s.library.UpdateItem(r.Context(), id, patch)
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "no such item")
	case err != nil:
		s.log.Error("editing an item", "error", err, "id", id)
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		writeJSON(w, http.StatusOK, itemResponse{Item: s.detail(r, item)})
	}
}

func (s *Server) deleteItem(w http.ResponseWriter, r *http.Request, id string) {
	err := s.library.SoftDeleteItem(r.Context(), id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "no such item")
	case err != nil:
		s.log.Error("deleting an item", "error", err, "id", id)
		writeError(w, http.StatusInternalServerError, "could not delete the item")
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) restoreItem(w http.ResponseWriter, r *http.Request, id string) {
	err := s.library.RestoreItem(r.Context(), id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "no such item")
	case err != nil:
		s.log.Error("restoring an item", "error", err, "id", id)
		writeError(w, http.StatusInternalServerError, "could not restore the item")
	default:
		item, err := s.library.GetItem(r.Context(), id)
		if err != nil {
			s.log.Error("reading a restored item", "error", err, "id", id)
			writeError(w, http.StatusInternalServerError, "the item was restored but could not be read")
			return
		}
		writeJSON(w, http.StatusOK, itemResponse{Item: s.detail(r, item)})
	}
}

// detail adds the fields only one item needs. Tags arrive with phase 9; the
// read-later flag is already real because the composer sets it.
func (s *Server) detail(r *http.Request, item model.Item) itemDTO {
	dto := toDTO(item)
	dto.ReadLater = s.library.InReadLater(r.Context(), item.ID)
	if tags, err := s.library.TagsForItem(r.Context(), item.ID); err == nil {
		dto.Tags = tags
	} else {
		s.log.Warn("tags for an item are unavailable", "error", err, "id", item.ID)
	}
	return dto
}

// --- /api/attention --------------------------------------------------------

type attentionResponse struct {
	Items []itemDTO `json:"items"`
}

// attention lists enrichment failures from the item rows, not from the log, so
// the strip survives a restart (design.md §6).
func (s *Server) attention(w http.ResponseWriter, r *http.Request) {
	if !s.requireLibrary(w) {
		return
	}
	items, err := s.library.FailedItems(r.Context(), 50)
	if err != nil {
		s.log.Error("listing failed items", "error", err)
		writeError(w, http.StatusInternalServerError, "could not read the attention list")
		return
	}
	response := attentionResponse{Items: make([]itemDTO, 0, len(items))}
	for _, item := range items {
		response.Items = append(response.Items, toDTO(item))
	}
	writeJSON(w, http.StatusOK, response)
}

// retryItem puts a failed item back in the queue. It is the one-tap retry in
// the attention strip; the enrichment loop picks it up on its next pass.
func (s *Server) retryItem(w http.ResponseWriter, r *http.Request, id string) {
	err := s.library.RetryItem(r.Context(), id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "no such item")
	case err != nil:
		s.log.Error("retrying an item", "error", err, "id", id)
		writeError(w, http.StatusInternalServerError, "could not retry the item")
	default:
		item, err := s.library.GetItem(r.Context(), id)
		if err != nil {
			s.log.Error("reading a retried item", "error", err, "id", id)
			writeError(w, http.StatusInternalServerError, "the item was retried but could not be read")
			return
		}
		writeJSON(w, http.StatusOK, itemResponse{Item: s.detail(r, item)})
	}
}

// --- helpers ---------------------------------------------------------------

// decodeJSON enforces one bounded JSON object with no unknown fields. A typo in
// a field name must fail loudly instead of being silently saved as nothing.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxItemBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		message := "body is not the expected JSON"
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			message = "body is too large"
		}
		writeError(w, http.StatusBadRequest, message)
		return false
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "body must be one JSON object")
		return false
	}
	return true
}

func parseItemsLimit(raw string) (int, error) {
	if strings.TrimSpace(raw) == "" {
		return defaultItemsLimit, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > maxItemsLimit {
		return 0, fmt.Errorf("limit must be between 1 and %d", maxItemsLimit)
	}
	return n, nil
}

// encodeCursor is opaque on purpose: the client stores it and hands it back,
// and cannot ask for an arbitrary (saved_at, seq) pair.
func encodeCursor(c store.Cursor) string {
	raw := c.SavedAt.UTC().Format(time.RFC3339Nano) + "|" + strconv.FormatInt(c.Seq, 10)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeCursor(raw string) (store.Cursor, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return store.Cursor{}, err
	}
	timestamp, seqRaw, ok := strings.Cut(string(decoded), "|")
	if !ok {
		return store.Cursor{}, errors.New("cursor has no separator")
	}
	savedAt, err := time.Parse(time.RFC3339Nano, timestamp)
	if err != nil {
		return store.Cursor{}, err
	}
	seq, err := strconv.ParseInt(seqRaw, 10, 64)
	if err != nil || seq <= 0 {
		return store.Cursor{}, errors.New("cursor has no seq")
	}
	return store.Cursor{SavedAt: savedAt.UTC(), Seq: seq}, nil
}
