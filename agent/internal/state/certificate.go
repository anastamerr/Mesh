package state

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// DeviceCertificate keeps the paired TLS key under the same OS protection as
// node credentials. Callers hold Store's identity lock throughout its use.
func (s *Store) DeviceCertificate() (tls.Certificate, string, error) {
	name := filepath.Join(s.dir, "device.key")
	var key *ecdsa.PrivateKey
	info, err := os.Lstat(name)
	if err == nil {
		if !info.Mode().IsRegular() || info.Size() > maxStateBytes || (runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
			return tls.Certificate{}, "", errors.New("invalid device key file")
		}
		data, readErr := os.ReadFile(name)
		if readErr != nil {
			return tls.Certificate{}, "", readErr
		}
		data, err = unprotect(data)
		if err == nil {
			key, err = x509.ParseECPrivateKey(data)
		}
	} else if os.IsNotExist(err) {
		key, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err == nil {
			var data []byte
			data, err = x509.MarshalECPrivateKey(key)
			if err == nil {
				err = s.saveProtected("device.key", data)
			}
		}
	}
	if err != nil {
		return tls.Certificate{}, "", errors.New("cannot load or persist device identity")
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, "", err
	}
	cert := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "Mesh device"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(1, 0, 0),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	private, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	pair, err := tls.X509KeyPair(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: private}))
	if err != nil {
		return tls.Certificate{}, "", err
	}
	public, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	hash := sha256.Sum256(public)
	return pair, hex.EncodeToString(hash[:]), nil
}
