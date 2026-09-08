package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
		data, err := readUploadBody(http.MaxBytesReader(w, r.Body, ChunkSize), r.ContentLength)
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
		start, end, partial, err := downloadRange(r.Header.Get("Range"), e.Size)
		if err != nil {
			w.Header().Set("Content-Range", "bytes */"+strconv.FormatInt(e.Size, 10))
			http.Error(w, http.StatusText(http.StatusRequestedRangeNotSatisfiable), http.StatusRequestedRangeNotSatisfiable)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Accept-Ranges", "bytes")
		length := end - start + 1
		w.Header().Set("Content-Length", strconv.FormatInt(length, 10))
		if partial {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, e.Size))
			w.WriteHeader(http.StatusPartialContent)
		}
		if _, err = f.Seek(start, io.SeekStart); err == nil {
			_, _ = io.CopyN(w, f, length)
		}
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

// downloadRange accepts one explicit byte range. Suffix and multipart ranges
// are unnecessary for transfer recovery and are rejected rather than guessed.
func downloadRange(value string, size int64) (start, end int64, partial bool, err error) {
	if value == "" {
		if size == 0 {
			return 0, -1, false, nil
		}
		return 0, size - 1, false, nil
	}
	if !strings.HasPrefix(value, "bytes=") || strings.Contains(value, ",") {
		return 0, 0, false, ErrInvalid
	}
	parts := strings.Split(strings.TrimPrefix(value, "bytes="), "-")
	if len(parts) != 2 || parts[0] == "" || size == 0 {
		return 0, 0, false, ErrInvalid
	}
	start, err = strconv.ParseInt(parts[0], 10, 64)
	if err != nil || start < 0 || start >= size {
		return 0, 0, false, ErrInvalid
	}
	end = size - 1
	if parts[1] != "" {
		end, err = strconv.ParseInt(parts[1], 10, 64)
		if err != nil || end < start || end >= size {
			return 0, 0, false, ErrInvalid
		}
	}
	return start, end, true, nil
}

// Known upload sizes need one allocation. Chunked legacy requests remain
// bounded, and both paths reject extra bytes before any file is changed.
func readUploadBody(body io.Reader, size int64) ([]byte, error) {
	if size < 0 {
		data, err := io.ReadAll(io.LimitReader(body, ChunkSize+1))
		if len(data) > ChunkSize {
			return nil, ErrInvalid
		}
		return data, err
	}
	if size > ChunkSize {
		return nil, ErrInvalid
	}
	data := make([]byte, size)
	if _, err := io.ReadFull(body, data); err != nil {
		return nil, err
	}
	var extra [1]byte
	if n, err := io.ReadFull(body, extra[:]); n != 0 || err != io.EOF {
		return nil, ErrInvalid
	}
	return data, nil
}
