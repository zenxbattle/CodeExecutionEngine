package executor

import (
	"sync"
	"time"
)

type RateLimiter struct {
	mu            sync.Mutex
	requests      map[string][]time.Time
	banned        map[string]time.Time
	maxRequests   int
	windowDur     time.Duration
	banDur        time.Duration
	stopCh        chan struct{}
}

func NewRateLimiter(maxRequests int, windowSec, banSec int) *RateLimiter {
	rl := &RateLimiter{
		requests:    make(map[string][]time.Time),
		banned:      make(map[string]time.Time),
		maxRequests: maxRequests,
		windowDur:   time.Duration(windowSec) * time.Second,
		banDur:      time.Duration(banSec) * time.Second,
		stopCh:      make(chan struct{}),
	}
	go rl.cleanupLoop()
	return rl
}

func (rl *RateLimiter) Stop() {
	close(rl.stopCh)
}

func (rl *RateLimiter) Allow(ip string) bool {
	if ip == "" {
		return true
	}
	rl.mu.Lock()
	defer rl.mu.Unlock()

	if until, ok := rl.banned[ip]; ok {
		if time.Now().Before(until) {
			return false
		}
		delete(rl.banned, ip)
	}

	now := time.Now()
	windowStart := now.Add(-rl.windowDur)

	entries := rl.requests[ip]
	var valid []time.Time
	for _, t := range entries {
		if t.After(windowStart) {
			valid = append(valid, t)
		}
	}

	if len(valid) >= rl.maxRequests {
		rl.banned[ip] = now.Add(rl.banDur)
		delete(rl.requests, ip)
		return false
	}

	valid = append(valid, now)
	rl.requests[ip] = valid
	return true
}

func (rl *RateLimiter) IsBlocked(ip string) bool {
	if ip == "" {
		return false
	}
	rl.mu.Lock()
	defer rl.mu.Unlock()
	until, ok := rl.banned[ip]
	if !ok {
		return false
	}
	if time.Now().Before(until) {
		return true
	}
	delete(rl.banned, ip)
	return false
}

func (rl *RateLimiter) CooldownRemaining(ip string) time.Duration {
	if ip == "" {
		return 0
	}
	rl.mu.Lock()
	defer rl.mu.Unlock()
	until, ok := rl.banned[ip]
	if !ok {
		return 0
	}
	rem := time.Until(until)
	if rem < 0 {
		delete(rl.banned, ip)
		return 0
	}
	return rem
}

func (rl *RateLimiter) cleanupLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-rl.stopCh:
			return
		case <-ticker.C:
			rl.mu.Lock()
			now := time.Now()
			for ip, until := range rl.banned {
				if now.After(until) {
					delete(rl.banned, ip)
				}
			}
			for ip, entries := range rl.requests {
				cutoff := now.Add(-rl.windowDur)
				var valid []time.Time
				for _, t := range entries {
					if t.After(cutoff) {
						valid = append(valid, t)
					}
				}
				if len(valid) == 0 {
					delete(rl.requests, ip)
				} else {
					rl.requests[ip] = valid
				}
			}
			rl.mu.Unlock()
		}
	}
}
