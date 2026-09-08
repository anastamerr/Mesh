package relay

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

type ServerConfig struct {
	Authorizer         Authorizer
	AuthTimeout        time.Duration
	QueueTimeout       time.Duration
	IdleTimeout        time.Duration
	MaxLifetime        time.Duration
	RevalidateInterval time.Duration
	MaxWaiting         int
	MaxWaitingPerNode  int
	MaxActive          int
	MaxActivePerNode   int
}

type Server struct {
	c            ServerConfig
	mu           sync.Mutex
	waiting      map[string][]*waitingDevice
	waitingCount int
	activeCount  int
	activeByNode map[string]int
	connections  map[net.Conn]struct{}
	closed       bool
	authSlots    chan struct{}
}

type waitingDevice struct {
	conn  net.Conn
	req   AuthRequest
	lease Lease
	done  chan struct{}
	once  sync.Once
}

func NewServer(c ServerConfig) (*Server, error) {
	if c.Authorizer == nil {
		return nil, errors.New("relay authorizer is required")
	}
	if c.AuthTimeout <= 0 {
		c.AuthTimeout = 5 * time.Second
	}
	if c.QueueTimeout <= 0 {
		c.QueueTimeout = 45 * time.Second
	}
	if c.IdleTimeout <= 0 {
		c.IdleTimeout = 2 * time.Minute
	}
	if c.MaxLifetime <= 0 {
		c.MaxLifetime = 30 * time.Minute
	}
	if c.RevalidateInterval <= 0 {
		c.RevalidateInterval = 15 * time.Second
	}
	if c.MaxWaiting <= 0 {
		c.MaxWaiting = 1024
	}
	if c.MaxWaitingPerNode <= 0 {
		c.MaxWaitingPerNode = 8
	}
	if c.MaxActive <= 0 {
		c.MaxActive = 512
	}
	if c.MaxActivePerNode <= 0 {
		c.MaxActivePerNode = 8
	}
	return &Server{c: c, waiting: make(map[string][]*waitingDevice), activeByNode: make(map[string]int), connections: make(map[net.Conn]struct{}), authSlots: make(chan struct{}, 64)}, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	role, nodeID, ok := route(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	// CONNECT carries the opaque stream only after the 2xx response. A
	// request body would otherwise be left behind when the request is
	// hijacked and could be mistaken for the beginning of that stream.
	if r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
		http.Error(w, "CONNECT request body is not allowed", http.StatusBadRequest)
		return
	}
	bearer, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok {
		w.Header().Set("WWW-Authenticate", "Bearer")
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	select {
	case s.authSlots <- struct{}{}:
	default:
		http.Error(w, "relay busy", http.StatusServiceUnavailable)
		return
	}
	authCtx, cancel := context.WithTimeout(r.Context(), s.c.AuthTimeout)
	authRequest := AuthRequest{Role: role, NodeID: nodeID, Bearer: bearer}
	lease, err := s.c.Authorizer.Authorize(authCtx, authRequest)
	cancel()
	<-s.authSlots
	if err != nil || !validLease(authRequest, lease) {
		status := http.StatusServiceUnavailable
		if errors.Is(err, ErrDenied) {
			status = http.StatusUnauthorized
		}
		http.Error(w, http.StatusText(status), status)
		return
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "HTTP/1.1 required", http.StatusHTTPVersionNotSupported)
		return
	}
	conn, rw, err := hijacker.Hijack()
	if err != nil {
		return
	}
	if !s.track(conn) {
		_ = conn.Close()
		s.untrack(conn)
		return
	}
	if rw.Reader.Buffered() != 0 {
		_ = conn.Close()
		s.untrack(conn)
		return
	}
	if role == RoleDevice {
		s.enqueueDevice(conn, authRequest, lease)
		return
	}
	s.connectConsumer(conn, rw.Writer, authRequest, lease)
}

func validLease(req AuthRequest, lease Lease) bool {
	return lease.Subject == string(req.Role)+":"+req.NodeID && lease.ExpiresAt.After(time.Now())
}

func (s *Server) enqueueDevice(conn net.Conn, req AuthRequest, lease Lease) {
	w := &waitingDevice{conn: conn, req: req, lease: lease, done: make(chan struct{})}
	s.mu.Lock()
	if s.closed || s.waitingCount >= s.c.MaxWaiting || len(s.waiting[req.NodeID]) >= s.c.MaxWaitingPerNode {
		s.mu.Unlock()
		writeStatus(conn, http.StatusServiceUnavailable)
		_ = conn.Close()
		s.untrack(conn)
		return
	}
	s.waiting[req.NodeID] = append(s.waiting[req.NodeID], w)
	s.waitingCount++
	s.mu.Unlock()
	go s.watchWaiting(w)
}

func (s *Server) watchWaiting(w *waitingDevice) {
	deadline := minTime(time.Now().Add(s.c.QueueTimeout), w.lease.ExpiresAt)
	timer := time.NewTimer(time.Until(deadline))
	ticker := time.NewTicker(s.c.RevalidateInterval)
	defer timer.Stop()
	defer ticker.Stop()
	for {
		select {
		case <-w.done:
			return
		case <-timer.C:
			s.removeWaiting(w, true)
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), s.c.AuthTimeout)
			lease, err := s.c.Authorizer.Revalidate(ctx, w.req, w.lease)
			cancel()
			if err != nil || lease.Subject != w.lease.Subject || !lease.ExpiresAt.After(time.Now()) {
				s.removeWaiting(w, false)
				return
			}
			// Retain the originally authorized expiry as a conservative hard
			// bound. A refreshed credential is picked up by the next waiter.
		}
	}
}

func (s *Server) removeWaiting(w *waitingDevice, expired bool) {
	s.mu.Lock()
	list := s.waiting[w.req.NodeID]
	found := false
	for i, candidate := range list {
		if candidate == w {
			found = true
			s.waiting[w.req.NodeID] = append(list[:i], list[i+1:]...)
			s.waitingCount--
			if len(s.waiting[w.req.NodeID]) == 0 {
				delete(s.waiting, w.req.NodeID)
			}
			break
		}
	}
	s.mu.Unlock()
	if !found {
		return
	}
	w.once.Do(func() {
		close(w.done)
		if expired {
			_ = w.conn.SetWriteDeadline(time.Now().Add(time.Second))
			_ = writeStatus(w.conn, http.StatusRequestTimeout)
		}
		_ = w.conn.Close()
	})
	s.untrack(w.conn)
}

func (s *Server) connectConsumer(conn net.Conn, writer *bufio.Writer, req AuthRequest, lease Lease) {
	var device *waitingDevice
	var expired []*waitingDevice
	s.mu.Lock()
	list := s.waiting[req.NodeID]
	now := time.Now()
	for len(list) > 0 && !list[0].lease.ExpiresAt.After(now) {
		expired = append(expired, list[0])
		list = list[1:]
		s.waitingCount--
	}
	if len(list) == 0 {
		delete(s.waiting, req.NodeID)
	} else {
		s.waiting[req.NodeID] = list
	}
	if lease.ExpiresAt.After(now) && len(list) > 0 && s.activeCount < s.c.MaxActive && s.activeByNode[req.NodeID] < s.c.MaxActivePerNode {
		device = list[0]
		s.waiting[req.NodeID] = list[1:]
		s.waitingCount--
		if len(list) == 1 {
			delete(s.waiting, req.NodeID)
		}
		s.activeCount++
		s.activeByNode[req.NodeID]++
	}
	s.mu.Unlock()
	for _, stale := range expired {
		stale.once.Do(func() {
			close(stale.done)
			_ = stale.conn.SetWriteDeadline(time.Now().Add(time.Second))
			_ = writeStatus(stale.conn, http.StatusRequestTimeout)
			_ = stale.conn.Close()
		})
		s.untrack(stale.conn)
	}
	if device == nil {
		writeStatusWriter(writer, http.StatusServiceUnavailable)
		_ = conn.Close()
		s.untrack(conn)
		return
	}
	device.once.Do(func() { close(device.done) })
	if writeStatus(device.conn, http.StatusOK) != nil || writeStatusWriter(writer, http.StatusOK) != nil {
		_ = device.conn.Close()
		_ = conn.Close()
		s.untrack(device.conn)
		s.untrack(conn)
		s.release(req.NodeID)
		return
	}
	s.bridge(req.NodeID, device.conn, conn, device.req, device.lease, req, lease)
}

func (s *Server) bridge(nodeID string, a, b net.Conn, ar AuthRequest, al Lease, br AuthRequest, bl Lease) {
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(s.c.RevalidateInterval)
		defer ticker.Stop()
		deadline := time.NewTimer(time.Until(minTime(time.Now().Add(s.c.MaxLifetime), minTime(al.ExpiresAt, bl.ExpiresAt))))
		defer deadline.Stop()
		for {
			select {
			case <-done:
				return
			case <-deadline.C:
				_ = a.Close()
				_ = b.Close()
				return
			case <-ticker.C:
				if !s.revalidate(ar, al) || !s.revalidate(br, bl) {
					_ = a.Close()
					_ = b.Close()
					return
				}
			}
		}
	}()
	idle := newIdleState(s.c.IdleTimeout, a, b)
	copyDone := make(chan struct{}, 2)
	activityA := &activityConn{Conn: a, idle: idle}
	activityB := &activityConn{Conn: b, idle: idle}
	go copyHalf(activityB, activityA, copyDone)
	go copyHalf(activityA, activityB, copyDone)
	<-copyDone
	_ = a.Close()
	_ = b.Close()
	<-copyDone
	close(done)
	s.untrack(a)
	s.untrack(b)
	s.release(nodeID)
}

func (s *Server) track(conn net.Conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || len(s.connections) >= s.connectionLimit() {
		return false
	}
	s.connections[conn] = struct{}{}
	return true
}

func (s *Server) connectionLimit() int {
	maxInt := int(^uint(0) >> 1)
	if s.c.MaxActive > (maxInt-s.c.MaxWaiting)/2 {
		return maxInt
	}
	return s.c.MaxWaiting + 2*s.c.MaxActive
}

func (s *Server) untrack(conn net.Conn) {
	s.mu.Lock()
	delete(s.connections, conn)
	s.mu.Unlock()
}

// Close terminates queued and active hijacked connections.
func (s *Server) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	var waiters []*waitingDevice
	for _, list := range s.waiting {
		waiters = append(waiters, list...)
	}
	s.waiting = make(map[string][]*waitingDevice)
	s.waitingCount = 0
	connections := make([]net.Conn, 0, len(s.connections))
	for conn := range s.connections {
		connections = append(connections, conn)
	}
	s.mu.Unlock()
	for _, waiter := range waiters {
		waiter.once.Do(func() { close(waiter.done) })
	}
	for _, conn := range connections {
		_ = conn.Close()
	}
	return nil
}

func (s *Server) revalidate(req AuthRequest, lease Lease) bool {
	ctx, cancel := context.WithTimeout(context.Background(), s.c.AuthTimeout)
	next, err := s.c.Authorizer.Revalidate(ctx, req, lease)
	cancel()
	return err == nil && next.Subject == lease.Subject && next.ExpiresAt.After(time.Now())
}

func (s *Server) release(nodeID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.activeCount--
	s.activeByNode[nodeID]--
	if s.activeByNode[nodeID] == 0 {
		delete(s.activeByNode, nodeID)
	}
}

type activityConn struct {
	net.Conn
	idle *idleState
}

func (c *activityConn) Read(p []byte) (int, error) {
	c.idle.setReadDeadline(c.Conn)
	n, err := c.Conn.Read(p)
	if n > 0 {
		c.idle.touch()
	}
	return n, err
}
func (c *activityConn) Write(p []byte) (int, error) {
	c.idle.setWriteDeadline(c.Conn)
	n, err := c.Conn.Write(p)
	if n > 0 {
		c.idle.touch()
	}
	return n, err
}

type idleState struct {
	mu      sync.Mutex
	timeout time.Duration
	last    time.Time
	conns   []net.Conn
}

func newIdleState(timeout time.Duration, conns ...net.Conn) *idleState {
	return &idleState{timeout: timeout, last: time.Now(), conns: conns}
}

func (s *idleState) setReadDeadline(conn net.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = conn.SetReadDeadline(s.last.Add(s.timeout))
}

func (s *idleState) setWriteDeadline(conn net.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = conn.SetWriteDeadline(s.last.Add(s.timeout))
}

func (s *idleState) touch() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.last = time.Now()
	deadline := s.last.Add(s.timeout)
	for _, conn := range s.conns {
		_ = conn.SetReadDeadline(deadline)
	}
}

func copyHalf(dst, src net.Conn, done chan<- struct{}) {
	buf := make([]byte, 32*1024)
	_, _ = io.CopyBuffer(dst, src, buf)
	if c, ok := dst.(interface{ CloseWrite() error }); ok {
		_ = c.CloseWrite()
	}
	done <- struct{}{}
}

func route(r *http.Request) (Role, string, bool) {
	if r.Method != http.MethodConnect {
		return "", "", false
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) != 5 || parts[0] != "v1" || parts[1] != "relay" || parts[2] != "nodes" {
		return "", "", false
	}
	role := Role(parts[4])
	return role, parts[3], validRole(role) && validNodeID(parts[3])
}

func validNodeID(id string) bool {
	if len(id) == 0 || len(id) > 128 {
		return false
	}
	for _, c := range id {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func bearerToken(header string) (string, bool) {
	if len(header) < 8 || len(header) > 8192 || !strings.HasPrefix(header, "Bearer ") {
		return "", false
	}
	token := strings.TrimPrefix(header, "Bearer ")
	return token, token != "" && !strings.ContainsAny(token, " \t\r\n")
}

func writeStatus(conn net.Conn, status int) error {
	return writeStatusWriter(bufio.NewWriter(conn), status)
}
func writeStatusWriter(w *bufio.Writer, status int) error {
	_, err := w.WriteString("HTTP/1.1 " + strconv.Itoa(status) + " " + http.StatusText(status) + "\r\nContent-Length: 0\r\n\r\n")
	if err == nil {
		err = w.Flush()
	}
	return err
}
func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
