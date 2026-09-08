package main

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"flag"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mesh.local/agent/internal/relay"
)

func TestRelayProcessHelper(t *testing.T) {
	if os.Getenv("MESH_RELAY_TEST_HELPER") != "1" {
		return
	}
	separator := 0
	for i, arg := range os.Args {
		if arg == "--" {
			separator = i + 1
			break
		}
	}
	if separator == 0 {
		os.Exit(2)
	}
	os.Args = append([]string{os.Args[0]}, os.Args[separator:]...)
	flag.CommandLine = flag.NewFlagSet(os.Args[0], flag.ExitOnError)
	if run() != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func TestRelayProcessCarriesOpaqueStream(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer relay-service" {
			http.Error(w, "denied", http.StatusUnauthorized)
			return
		}
		var input struct {
			Role   relay.Role `json:"role"`
			NodeID string     `json:"nodeId"`
			Token  string     `json:"token"`
		}
		if json.NewDecoder(r.Body).Decode(&input) != nil || input.NodeID != "node-1" || input.Token != string(input.Role)+"-token" {
			http.Error(w, "denied", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(relay.Lease{Subject: string(input.Role) + ":node-1", ExpiresAt: time.Now().Add(time.Minute)})
	}))
	defer auth.Close()

	dir := t.TempDir()
	certFile, keyFile, roots := testCertificate(t, dir)
	serviceFile := filepath.Join(dir, "service-token")
	if err := os.WriteFile(serviceFile, []byte("relay-service\n"), 0600); err != nil {
		t.Fatal(err)
	}
	address := reserveAddress(t)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, "-test.run=^TestRelayProcessHelper$", "--", "--listen", address, "--tls-cert", certFile, "--tls-key", keyFile, "--auth-url", auth.URL, "--auth-service-token-file", serviceFile)
	cmd.Env = append(os.Environ(), "MESH_RELAY_TEST_HELPER=1")
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	ready := make(chan bool, 1)
	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			if strings.HasPrefix(scanner.Text(), "Relay listening on ") {
				ready <- true
				return
			}
		}
		ready <- false
	}()
	select {
	case ok := <-ready:
		if !ok {
			t.Fatal("relay process exited before ready")
		}
	case <-ctx.Done():
		t.Fatal("relay process did not become ready")
	}

	// A CONNECT request body is HTTP framing, not part of the opaque stream.
	// Send the headers first so this also exercises the late-body case where
	// the bytes have not reached the relay when it decides whether to hijack.
	framingConn, err := tls.Dial("tcp", address, &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatal(err)
	}
	_ = framingConn.SetDeadline(time.Now().Add(5 * time.Second))
	_, err = io.WriteString(framingConn, "CONNECT /v1/relay/nodes/node-1/device HTTP/1.1\r\nHost: "+address+"\r\nAuthorization: Bearer device-token\r\nContent-Length: 4\r\n\r\n")
	if err != nil {
		_ = framingConn.Close()
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if _, err = io.WriteString(framingConn, "evil"); err != nil {
		_ = framingConn.Close()
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(framingConn), &http.Request{Method: http.MethodConnect})
	if err != nil {
		_ = framingConn.Close()
		t.Fatal(err)
	}
	_ = response.Body.Close()
	_ = framingConn.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("CONNECT body was accepted with status %d", response.StatusCode)
	}

	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	go func() {
		conn, acceptErr := target.Accept()
		if acceptErr == nil {
			defer conn.Close()
			_, _ = io.Copy(conn, conn)
		}
	}()
	deviceCtx, stopDevice := context.WithCancel(ctx)
	defer stopDevice()
	go func() {
		_ = relay.ServeDevice(deviceCtx, relay.DeviceConfig{
			RelayOrigin: "https://" + address,
			NodeID:      "node-1",
			TLSConfig:   &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
			Workers:     1,
			Credential:  func(context.Context) (string, error) { return "device-token", nil },
			DialTarget: func(ctx context.Context) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "tcp", target.Addr().String())
			},
		})
	}()

	var consumer net.Conn
	for consumer == nil && ctx.Err() == nil {
		consumer, err = relay.Dial(ctx, "https://"+address, "node-1", relay.RoleConsumer, "consumer-token", &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12})
		if err != nil {
			time.Sleep(25 * time.Millisecond)
		}
	}
	if consumer == nil {
		t.Fatal("consumer did not connect", err)
	}
	defer consumer.Close()
	payload := []byte{0, 1, 2, 255, '\r', '\n', 0, 42}
	if _, err = consumer.Write(payload); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(payload))
	if _, err = io.ReadFull(consumer, got); err != nil || string(got) != string(payload) {
		t.Fatalf("opaque stream mismatch: %v %v", got, err)
	}
}

func reserveAddress(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := l.Addr().String()
	_ = l.Close()
	return address
}

func testCertificate(t *testing.T, dir string) (string, string, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Mesh relay test"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	certFile, keyFile := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if err = os.WriteFile(certFile, certPEM, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(keyFile, keyPEM, 0600); err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(certPEM)
	return certFile, keyFile, roots
}
