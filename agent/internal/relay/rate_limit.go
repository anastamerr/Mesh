package relay

import (
	"math"
	"sync"
	"time"
)

type tokenBucket struct {
	tokens float64
	last   time.Time
}

type connectLimiter struct {
	mu             sync.Mutex
	global         tokenBucket
	perNode        map[string]tokenBucket
	globalPerMin   int
	globalBurst    int
	perNodePerMin  int
	perNodeBurst   int
	maxTrackedNode int
}

func newConnectLimiter(globalPerMin, globalBurst, perNodePerMin, perNodeBurst int) *connectLimiter {
	now := time.Now()
	return &connectLimiter{
		global:         tokenBucket{tokens: float64(globalBurst), last: now},
		perNode:        make(map[string]tokenBucket),
		globalPerMin:   globalPerMin,
		globalBurst:    globalBurst,
		perNodePerMin:  perNodePerMin,
		perNodeBurst:   perNodeBurst,
		maxTrackedNode: 20_000,
	}
}

func (l *connectLimiter) check(nodeID string) (bool, int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	global, globalWait := takeToken(l.global, l.globalPerMin, l.globalBurst, now)
	if globalWait > 0 {
		l.global = global
		return false, globalWait
	}
	bucket, found := l.perNode[nodeID]
	if !found {
		if len(l.perNode) >= l.maxTrackedNode {
			for key, candidate := range l.perNode {
				if now.Sub(candidate.last) > 10*time.Minute {
					delete(l.perNode, key)
				}
			}
			if len(l.perNode) >= l.maxTrackedNode {
				return false, 60
			}
		}
		bucket = tokenBucket{tokens: float64(l.perNodeBurst), last: now}
	}
	next, nodeWait := takeToken(bucket, l.perNodePerMin, l.perNodeBurst, now)
	if nodeWait > 0 {
		l.perNode[nodeID] = next
		return false, nodeWait
	}
	l.global = global
	l.perNode[nodeID] = next
	return true, 0
}

func takeToken(bucket tokenBucket, perMinute, burst int, now time.Time) (tokenBucket, int) {
	elapsed := now.Sub(bucket.last).Seconds()
	bucket.tokens = math.Min(float64(burst), bucket.tokens+elapsed*float64(perMinute)/60)
	bucket.last = now
	if bucket.tokens >= 1 {
		bucket.tokens--
		return bucket, 0
	}
	wait := int(math.Ceil((1 - bucket.tokens) * 60 / float64(perMinute)))
	if wait < 1 {
		wait = 1
	}
	return bucket, wait
}
