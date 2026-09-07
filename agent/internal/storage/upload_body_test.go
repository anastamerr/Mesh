package storage

import (
	"bytes"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestUploadBodyFramingBeforeMutation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		body   string
		length int64
		status int
	}{
		{"known", "abc", 3, 200},
		{"chunked", "abc", -1, 200},
		{"truncated", "ab", 3, 400},
		{"trailing", "abcd", 3, 400},
		{"oversized-declaration", "abc", ChunkSize + 1, 400},
		{"oversized-chunked", strings.Repeat("x", ChunkSize+1), -1, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := openStore(t, privateDir(t))
			defer s.Close()
			m := Manifest{Version: 1, Entries: []Entry{entry("file", []byte("abc"))}}
			p := begin(t, s, m)
			r := httptest.NewRequest("PUT", "/v1/collections/"+p.ID+"/files/0", strings.NewReader(tc.body))
			r.ContentLength = tc.length
			r.Header.Set("Authorization", "Bearer "+testKey)
			r.Header.Set("Upload-Offset", "0")
			w := httptest.NewRecorder()
			Handler(s, testKey).ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("status = %d, want %d", w.Code, tc.status)
			}
			got := begin(t, s, m)
			if tc.status == 200 {
				if got.Offsets[0] != 3 {
					t.Fatal("valid body not acknowledged")
				}
			} else {
				if got.Offsets[0] != 0 {
					t.Fatal("invalid body advanced durable offset")
				}
				if _, err := s.root.Stat("staging/" + p.ID + "/file"); !os.IsNotExist(err) {
					t.Fatalf("invalid body touched file: %v", err)
				}
			}
		})
	}
}

func TestBatchRejectsMissingOrNonzeroSelectedOffsets(t *testing.T) {
	for _, fault := range []string{"missing", "partial", "complete"} {
		t.Run(fault, func(t *testing.T) {
			s := openStore(t, privateDir(t))
			defer s.Close()
			m := Manifest{Version: 1, Entries: make([]Entry, 200)}
			for i := range m.Entries {
				m.Entries[i] = entry(fmt.Sprintf("%03d", i), []byte("ab"))
			}
			p := begin(t, s, m)
			query := "DELETE FROM files WHERE ordinal=199"
			if fault == "partial" {
				query = "UPDATE files SET offset=1 WHERE ordinal=199"
			} else if fault == "complete" {
				query = "UPDATE files SET offset=2 WHERE ordinal=199"
			}
			if _, err := s.db.Exec(query); err != nil {
				t.Fatal(err)
			}
			if _, err := s.writeBatch(testContext, p.ID, m, []int{198, 199}, []byte("abab")); !errors.Is(err, ErrConflict) {
				t.Fatalf("bad selected offset accepted: %v", err)
			}
			if _, err := s.root.Stat("staging/" + p.ID + "/198"); !os.IsNotExist(err) {
				t.Fatal("batch mutated files before offset validation")
			}
		})
	}
}

func TestBatchBodyAcceptsExactLimit(t *testing.T) {
	data := bytes.Repeat([]byte("x"), ChunkSize)
	got, err := readUploadBody(bytes.NewReader(data), ChunkSize)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("full-size body rejected: %v", err)
	}
}
