package authlimit

import (
	"container/list"
	"strings"
	"sync"
	"time"
)

type Config struct {
	GlobalBurst     int
	GlobalRefill    time.Duration
	IPBurst         int
	IPRefill        time.Duration
	MaxIPs          int
	MaxAccounts     int
	EntryTTL        time.Duration
	CleanupInterval time.Duration
}

type Decision struct {
	Allowed    bool
	Delay      time.Duration
	RetryAfter time.Duration
}

type bucket struct {
	tokens float64
	last   time.Time
}

type ipEntry struct {
	bucket     bucket
	lastAccess time.Time
	element    *list.Element
}

type accountEntry struct {
	failures   int
	lastAccess time.Time
	element    *list.Element
}

type Limiter struct {
	mu          sync.Mutex
	cfg         Config
	now         func() time.Time
	global      bucket
	ips         map[string]*ipEntry
	ipLRU       *list.List
	accounts    map[string]*accountEntry
	accountLRU  *list.List
	nextCleanup time.Time
}

func DefaultConfig() Config {
	return Config{
		GlobalBurst:     30,
		GlobalRefill:    time.Second,
		IPBurst:         10,
		IPRefill:        90 * time.Second,
		MaxIPs:          4096,
		MaxAccounts:     1024,
		EntryTTL:        30 * time.Minute,
		CleanupInterval: time.Minute,
	}
}

func New(cfg Config) *Limiter { return NewWithClock(cfg, time.Now) }

func NewWithClock(cfg Config, now func() time.Time) *Limiter {
	defaults := DefaultConfig()
	if cfg.GlobalBurst <= 0 {
		cfg.GlobalBurst = defaults.GlobalBurst
	}
	if cfg.GlobalRefill <= 0 {
		cfg.GlobalRefill = defaults.GlobalRefill
	}
	if cfg.IPBurst <= 0 {
		cfg.IPBurst = defaults.IPBurst
	}
	if cfg.IPRefill <= 0 {
		cfg.IPRefill = defaults.IPRefill
	}
	if cfg.MaxIPs <= 0 {
		cfg.MaxIPs = defaults.MaxIPs
	}
	if cfg.MaxAccounts <= 0 {
		cfg.MaxAccounts = defaults.MaxAccounts
	}
	if cfg.EntryTTL <= 0 {
		cfg.EntryTTL = defaults.EntryTTL
	}
	if cfg.CleanupInterval <= 0 {
		cfg.CleanupInterval = defaults.CleanupInterval
	}
	current := now()
	return &Limiter{
		cfg: cfg, now: now,
		global: bucket{tokens: float64(cfg.GlobalBurst), last: current},
		ips:    map[string]*ipEntry{}, ipLRU: list.New(),
		accounts: map[string]*accountEntry{}, accountLRU: list.New(),
		nextCleanup: current.Add(cfg.CleanupInterval),
	}
}

func refill(value *bucket, capacity int, interval time.Duration, now time.Time) {
	if now.Before(value.last) {
		value.last = now
		return
	}
	value.tokens += now.Sub(value.last).Seconds() / interval.Seconds()
	if value.tokens > float64(capacity) {
		value.tokens = float64(capacity)
	}
	value.last = now
}

func (l *Limiter) Allow(ip, username string) Decision {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.cleanup(now)
	ip = strings.TrimSpace(ip)
	if ip == "" {
		ip = "unknown"
	}
	username = strings.ToLower(strings.TrimSpace(username))

	refill(&l.global, l.cfg.GlobalBurst, l.cfg.GlobalRefill, now)
	entry := l.ip(ip, now)
	refill(&entry.bucket, l.cfg.IPBurst, l.cfg.IPRefill, now)
	if l.global.tokens < 1 || entry.bucket.tokens < 1 {
		retryAfter := time.Duration(0)
		if l.global.tokens < 1 {
			retryAfter = timeToToken(l.global.tokens, l.cfg.GlobalRefill)
		}
		if entry.bucket.tokens < 1 {
			ipRetry := timeToToken(entry.bucket.tokens, l.cfg.IPRefill)
			if ipRetry > retryAfter {
				retryAfter = ipRetry
			}
		}
		return Decision{Allowed: false, RetryAfter: retryAfter}
	}
	l.global.tokens--
	entry.bucket.tokens--

	account := l.account(username, now)
	delay := failureDelay(account.failures)
	return Decision{Allowed: true, Delay: delay}
}

func timeToToken(tokens float64, interval time.Duration) time.Duration {
	missing := 1 - tokens
	if missing <= 0 {
		return 0
	}
	return time.Duration(missing * float64(interval))
}

func (l *Limiter) Fail(username string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	entry := l.account(strings.ToLower(strings.TrimSpace(username)), now)
	if entry.failures < 1000 {
		entry.failures++
	}
}

func (l *Limiter) Success(username string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	key := strings.ToLower(strings.TrimSpace(username))
	if entry, ok := l.accounts[key]; ok {
		l.accountLRU.Remove(entry.element)
		delete(l.accounts, key)
	}
}

func failureDelay(failures int) time.Duration {
	switch {
	case failures < 5:
		return 0
	case failures == 5:
		return 250 * time.Millisecond
	case failures == 6:
		return 500 * time.Millisecond
	case failures == 7:
		return time.Second
	default:
		return 2 * time.Second
	}
}

func (l *Limiter) ip(key string, now time.Time) *ipEntry {
	if entry, ok := l.ips[key]; ok {
		entry.lastAccess = now
		l.ipLRU.MoveToFront(entry.element)
		return entry
	}
	for len(l.ips) >= l.cfg.MaxIPs {
		l.evictIP()
	}
	entry := &ipEntry{bucket: bucket{tokens: float64(l.cfg.IPBurst), last: now}, lastAccess: now}
	entry.element = l.ipLRU.PushFront(key)
	l.ips[key] = entry
	return entry
}

func (l *Limiter) account(key string, now time.Time) *accountEntry {
	if entry, ok := l.accounts[key]; ok {
		entry.lastAccess = now
		l.accountLRU.MoveToFront(entry.element)
		return entry
	}
	for len(l.accounts) >= l.cfg.MaxAccounts {
		l.evictAccount()
	}
	entry := &accountEntry{lastAccess: now}
	entry.element = l.accountLRU.PushFront(key)
	l.accounts[key] = entry
	return entry
}

func (l *Limiter) evictIP() {
	oldest := l.ipLRU.Back()
	if oldest == nil {
		return
	}
	delete(l.ips, oldest.Value.(string))
	l.ipLRU.Remove(oldest)
}

func (l *Limiter) evictAccount() {
	oldest := l.accountLRU.Back()
	if oldest == nil {
		return
	}
	delete(l.accounts, oldest.Value.(string))
	l.accountLRU.Remove(oldest)
}

func (l *Limiter) cleanup(now time.Time) {
	if now.Before(l.nextCleanup) {
		return
	}
	cutoff := now.Add(-l.cfg.EntryTTL)
	for key, entry := range l.ips {
		if entry.lastAccess.Before(cutoff) {
			l.ipLRU.Remove(entry.element)
			delete(l.ips, key)
		}
	}
	for key, entry := range l.accounts {
		if entry.lastAccess.Before(cutoff) {
			l.accountLRU.Remove(entry.element)
			delete(l.accounts, key)
		}
	}
	l.nextCleanup = now.Add(l.cfg.CleanupInterval)
}

func (l *Limiter) Counts() (ips, accounts int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.ips), len(l.accounts)
}
