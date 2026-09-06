package storage

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"net/http"
)

var (
	ErrUnauthorized             = errors.New("storage permission denied")
	ErrAuthorizationUnavailable = errors.New("storage authorization unavailable")
)

type Permission struct {
	Access       string  `json:"access"`
	CollectionID *string `json:"collectionId"`
}

type Authorizer func(context.Context, string, Permission) error

// Handler is the loopback development mode. Enrolled serving uses
// AuthorizedHandler with controller validation instead of a shared root key.
func Handler(store *Store, key string) http.Handler {
	return AuthorizedHandler(store, LocalAuthorizer(key), nil)
}

func LocalAuthorizer(key string) Authorizer {
	expected := sha256.Sum256([]byte(key))
	return func(_ context.Context, token string, _ Permission) error {
		actual := sha256.Sum256([]byte(token))
		if !ValidKey(key) || subtle.ConstantTimeCompare(expected[:], actual[:]) != 1 {
			return ErrUnauthorized
		}
		return nil
	}
}
