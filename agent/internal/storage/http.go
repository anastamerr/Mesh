package storage

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
)

func jsonReply(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}
func storageError(w http.ResponseWriter, err error) {
	code := http.StatusInternalServerError
	switch {
	case errors.Is(err, ErrInvalid):
		code = 400
	case errors.Is(err, ErrNotFound):
		code = 404
	case errors.Is(err, ErrConflict):
		code = 409
	}
	http.Error(w, http.StatusText(code), code)
}

// Handler uses a separate operator storage key. Node heartbeat credentials are
// deliberately not accepted here. A future gateway can replace this boundary.
func Handler(store *Store, key string) http.Handler {
	expected := sha256.Sum256([]byte("Bearer " + key))
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/collections", func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxManifestBytes))
		var m Manifest
		if err != nil || decodeManifest(data, &m) != nil {
			storageError(w, ErrInvalid)
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
		ids, err := store.List(r.Context(), r.URL.Query().Get("after"))
		if err != nil {
			storageError(w, err)
			return
		}
		jsonReply(w, ids)
	})
	mux.HandleFunc("GET /v1/collections/{id}", func(w http.ResponseWriter, r *http.Request) {
		m, _, err := store.manifest(r.Context(), r.PathValue("id"), true)
		if err != nil {
			storageError(w, err)
			return
		}
		jsonReply(w, m)
	})
	mux.HandleFunc("POST /v1/collections/{id}/finish", func(w http.ResponseWriter, r *http.Request) {
		p, err := store.Finish(r.Context(), r.PathValue("id"))
		if err != nil {
			storageError(w, err)
			return
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
		w.Header().Set("X-Content-Type-Options", "nosniff")
		got := sha256.Sum256([]byte(r.Header.Get("Authorization")))
		if !ValidKey(key) || subtle.ConstantTimeCompare(expected[:], got[:]) != 1 {
			http.Error(w, "Unauthorized", 401)
			return
		}
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			http.Error(w, "Busy", 503)
			return
		}
		mux.ServeHTTP(w, r)
	})
}
