package cli

import (
	"errors"
	"io"
	"regexp"
	"strings"
)

var operatorToken = regexp.MustCompile(`^[A-Za-z0-9_-]{32,256}$`)

func readOperatorCredential(input io.Reader) (string, error) {
	data, err := io.ReadAll(io.LimitReader(input, 257))
	if err != nil || len(data) > 256 {
		return "", errors.New("invalid operator credential")
	}
	credential := strings.TrimSpace(string(data))
	if !operatorToken.MatchString(credential) {
		return "", errors.New("invalid operator credential")
	}
	return credential, nil
}
