package storage

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
)

func jsonReply(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}
func storageError(w http.ResponseWriter, err error) {
	code := http.StatusInternalServerError
	switch {
	case errors.Is(err, ErrUnauthorized):
		code = 401
	case errors.Is(err, ErrAuthorizationUnavailable):
		code = 503
		w.Header().Set("Mesh-Error", "authorization-unavailable")
	case errors.Is(err, ErrInvalid):
		code = 400
	case errors.Is(err, ErrNotFound):
		code = 404
	case errors.Is(err, ErrConflict):
		code = 409
	}
	http.Error(w, http.StatusText(code), code)
}

type PublicationReporter func(context.Context, string, Manifest) error

// AuthorizedHandler checks scope before touching collection state. It does not
// cache decisions: revocation and expiry apply to each subsequent request.
func AuthorizedHandler(store *Store, authorize Authorizer, report PublicationReporter) http.Handler {
	allow := func(w http.ResponseWriter, r *http.Request, access, id string) bool {
		permission := Permission{Access: access}
		if access != "list" {
			if !validID(id) {
				storageError(w, ErrInvalid)
				return false
			}
			permission.CollectionID = &id
		}
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if err := authorize(r.Context(), token, permission); err != nil {
			storageError(w, err)
			return false
		}
		return true
	}
	mux := http.NewServeMux()
	for _, method := range []string{"GET", "PUT"} {
		mux.HandleFunc(method+" /v1/collections/{id}/batch", func(w http.ResponseWriter, r *http.Request) {
			access := "read"
			if r.Method == "PUT" {
				access = "write"
			}
			if allow(w, r, access, r.PathValue("id")) {
				store.batchHandler(w, r)
			}
		})
	}

	mux.HandleFunc("POST /v1/collections", func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxManifestBytes))
		var m Manifest
		if err != nil || decodeManifest(data, &m) != nil {
			storageError(w, ErrInvalid)
			return
		}
		id, err := m.ID()
		if err != nil {
			storageError(w, err)
			return
		}
		if !allow(w, r, "write", id) {
			return
		}
		p, err := store.Begin(r.Context(), m)
		if err != nil {
			storageError(w, err)
			return
		}
		jsonReply(w, p)
	})
	mux.HandleFunc("GET /v1/collections", func(w http.ResponseWriter, r *http.Request) {
		if !allow(w, r, "list", "") {
			return
		}
		ids, err := store.List(r.Context(), r.URL.Query().Get("after"))
		if err != nil {
			storageError(w, err)
			return
		}
		jsonReply(w, ids)
	})
	mux.HandleFunc("GET /v1/collections/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !allow(w, r, "read", r.PathValue("id")) {
			return
		}
		m, _, err := store.manifest(r.Context(), r.PathValue("id"), true)
		if err != nil {
			storageError(w, err)
			return
		}
		jsonReply(w, m)
	})
	mux.HandleFunc("POST /v1/collections/{id}/finish", func(w http.ResponseWriter, r *http.Request) {
		if !allow(w, r, "write", r.PathValue("id")) {
			return
		}
		p, err := store.Finish(r.Context(), r.PathValue("id"))
		if err != nil {
			storageError(w, err)
			return
		}
		if report != nil {
			manifest, _, err := store.manifest(r.Context(), p.ID, true)
			if err == nil {
				err = report(r.Context(), p.ID, manifest)
			}
			if err != nil {
				storageError(w, ErrAuthorizationUnavailable)
				return
			}
		}
		jsonReply(w, p)
	})
	mux.HandleFunc("PUT /v1/collections/{id}/files/{index}", func(w http.ResponseWriter, r *http.Request) {
		index, err := strconv.Atoi(r.PathValue("index"))
		offset, offsetErr := strconv.ParseInt(r.Header.Get("Upload-Offset"), 10, 64)
		if err != nil || offsetErr != nil {
			storageError(w, ErrInvalid)
			return
		}
		if !allow(w, r, "write", r.PathValue("id")) {
			return
		}
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, ChunkSize))
		if err != nil {
			storageError(w, ErrInvalid)
			return
		}
		next, err := store.Write(r.Context(), r.PathValue("id"), index, offset, data)
		if err != nil {
			storageError(w, err)
			return
		}
		jsonReply(w, struct {
			Offset int64 `json:"offset"`
		}{next})
	})
	mux.HandleFunc("GET /v1/collections/{id}/files/{index}", func(w http.ResponseWriter, r *http.Request) {
		index, err := strconv.Atoi(r.PathValue("index"))
		if err != nil {
			storageError(w, ErrInvalid)
			return
		}
		if !allow(w, r, "read", r.PathValue("id")) {
			return
		}
		f, e, err := store.Read(r.Context(), r.PathValue("id"), index)
		if err != nil {
			storageError(w, err)
			return
		}
		defer f.Close()
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.FormatInt(e.Size, 10))
		_, _ = io.CopyN(w, f, e.Size)
	})
	// Limit active requests before reading bodies: memory stays bounded even when
	// several clients upload concurrently. Excess clients may retry later.
	slots := make(chan struct{}, 4)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Mesh-Transfer-Features", batchFeature)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		header := r.Header.Get("Authorization")
		if !strings.HasPrefix(header, "Bearer ") || !ValidKey(strings.TrimPrefix(header, "Bearer ")) {
			storageError(w, ErrUnauthorized)
			return
		}
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			w.Header().Set("Mesh-Error", "busy")
			w.Header().Set("Retry-After", "1")
			http.Error(w, "Busy", 503)
			return
		}
		mux.ServeHTTP(w, r)
	})
}
