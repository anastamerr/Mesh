package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEveryStorageOperationRequiresItsExactScope(t *testing.T) {
	s := openStore(t, privateDir(t))
	defer s.Close()
	m := Manifest{Version: 1, Entries: []Entry{entry("file", []byte("x"))}}
	id, err := m.ID()
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		method, path, access string
		body                 []byte
	}{
		{"POST", "/v1/collections", "write", body},
		{"PUT", "/v1/collections/" + id + "/files/0", "write", []byte("x")},
		{"POST", "/v1/collections/" + id + "/finish", "write", nil},
		{"GET", "/v1/collections/" + id, "read", nil},
		{"GET", "/v1/collections/" + id + "/files/0", "read", nil},
		{"GET", "/v1/collections", "list", nil},
	}
	for _, tc := range cases {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			calls := 0
			handler := AuthorizedHandler(s, func(_ context.Context, token string, permission Permission) error {
				calls++
				if token != testKey || permission.Access != tc.access {
					t.Fatal("wrong authorization request")
				}
				if tc.access == "list" {
					if permission.CollectionID != nil {
						t.Fatal("list scoped to a collection")
					}
				} else if permission.CollectionID == nil || *permission.CollectionID != id {
					t.Fatal("wrong collection")
				}
				return ErrUnauthorized
			})
			req := httptest.NewRequest(tc.method, tc.path, bytes.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer "+testKey)
			req.Header.Set("Upload-Offset", "0")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, req)
			if response.Code != 401 || calls != 1 {
				t.Fatal(response.Code, calls)
			}
		})
	}
	var count int
	if err = s.db.QueryRow("SELECT count(*) FROM collections").Scan(&count); err != nil || count != 0 {
		t.Fatal("denied request mutated storage", count, err)
	}
}

func TestAuthorizationUnavailableFailsClosed(t *testing.T) {
	s := openStore(t, privateDir(t))
	defer s.Close()
	handler := AuthorizedHandler(s, func(context.Context, string, Permission) error { return ErrAuthorizationUnavailable })
	req := httptest.NewRequest(http.MethodGet, "/v1/collections", nil)
	req.Header.Set("Authorization", "Bearer "+testKey)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code != 503 {
		t.Fatal(response.Code)
	}
}
