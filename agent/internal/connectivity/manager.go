package connectivity

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"
)

type Path string

const (
	PathDirect Path = "direct"
	PathRelay  Path = "relay"
)

type Route struct {
	Path Path
	Dial func(context.Context) (net.Conn, error)
}

type Manager struct {
	Direct          []Route
	Relay           *Route
	DirectHeadStart time.Duration
	OnSelected      func(Path)
}

type attempt struct {
	path Path
	conn net.Conn
	err  error
}

var ErrNoRoute = errors.New("no usable direct or relay route to device")

// Dial races a bounded set of authenticated direct routes, then starts the
// authenticated relay fallback after a short head start. Only connection
// establishment is raced; an established transfer is never migrated.
func (m *Manager) Dial(ctx context.Context, _, _ string) (net.Conn, error) {
	direct := m.Direct
	if len(direct) > 4 {
		direct = direct[:4]
	}
	count := len(direct)
	if m.Relay != nil {
		count++
	}
	if count == 0 {
		return nil, ErrNoRoute
	}
	child, cancel := context.WithCancel(ctx)
	results := make(chan attempt, count)
	start := func(route Route, delay time.Duration) {
		go func() {
			if delay > 0 {
				timer := time.NewTimer(delay)
				select {
				case <-child.Done():
					timer.Stop()
					results <- attempt{path: route.Path, err: child.Err()}
					return
				case <-timer.C:
				}
			}
			conn, err := route.Dial(child)
			if err == nil && child.Err() != nil {
				_ = conn.Close()
				conn, err = nil, child.Err()
			}
			results <- attempt{path: route.Path, conn: conn, err: err}
		}()
	}
	for _, route := range direct {
		route.Path = PathDirect
		start(route, 0)
	}
	if m.Relay != nil {
		delay := m.DirectHeadStart
		if len(direct) == 0 || delay < 0 {
			delay = 0
		}
		route := *m.Relay
		route.Path = PathRelay
		start(route, delay)
	}
	for remaining := count; remaining > 0; remaining-- {
		select {
		case result := <-results:
			if result.err != nil {
				continue
			}
			cancel()
			// Close any authenticated connection that loses the establishment race.
			go func(left int) {
				for range left {
					late := <-results
					if late.conn != nil {
						_ = late.conn.Close()
					}
				}
			}(remaining - 1)
			if m.OnSelected != nil {
				m.OnSelected(result.path)
			}
			return result.conn, nil
		case <-ctx.Done():
			cancel()
			return nil, ctx.Err()
		}
	}
	cancel()
	return nil, ErrNoRoute
}

// PathReporter emits only route changes, avoiding noisy per-HTTP-connection logs.
type PathReporter struct {
	mu   sync.Mutex
	last Path
	Log  func(Path)
}

func (r *PathReporter) Selected(path Path) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if path == r.last {
		return
	}
	r.last = path
	if r.Log != nil {
		r.Log(path)
	}
}
