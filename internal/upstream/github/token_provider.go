package github

import (
	"sync"
	"sync/atomic"
	"time"
)

// TokenProvider defines token selection for upstream API requests.
type TokenProvider interface {
	NextToken() string
}

const defaultRateLimitCooldown = time.Minute

// RoundRobinTokenProvider rotates through tokens in deterministic order.
type RoundRobinTokenProvider struct {
	tokens           []string
	tokenSet         map[string]struct{}
	nextIndex        uint64
	threshold        int
	cooldownDuration time.Duration
	now              func() time.Time

	mu            sync.RWMutex
	remaining     map[string]int
	cooldownUntil map[string]time.Time
}

// NewRoundRobinTokenProvider creates a round-robin provider for the given tokens.
func NewRoundRobinTokenProvider(tokens []string) *RoundRobinTokenProvider {
	return newTokenProvider(tokens, 0, defaultRateLimitCooldown, time.Now)
}

// NewThresholdTokenProvider creates a round-robin provider with threshold preference.
func NewThresholdTokenProvider(tokens []string, threshold int) *RoundRobinTokenProvider {
	return newTokenProvider(tokens, threshold, defaultRateLimitCooldown, time.Now)
}

func newTokenProvider(
	tokens []string,
	threshold int,
	cooldownDuration time.Duration,
	now func() time.Time,
) *RoundRobinTokenProvider {
	provider := &RoundRobinTokenProvider{
		tokens:           make([]string, 0, len(tokens)),
		tokenSet:         make(map[string]struct{}, len(tokens)),
		threshold:        threshold,
		cooldownDuration: cooldownDuration,
		now:              now,
		remaining:        make(map[string]int, len(tokens)),
		cooldownUntil:    make(map[string]time.Time, len(tokens)),
	}
	if provider.now == nil {
		provider.now = time.Now
	}

	for _, token := range tokens {
		if token == "" {
			continue
		}
		if _, exists := provider.tokenSet[token]; exists {
			continue
		}
		provider.tokens = append(provider.tokens, token)
		provider.tokenSet[token] = struct{}{}
	}

	return provider
}

// NextToken returns the next token in rotation.
func (p *RoundRobinTokenProvider) NextToken() string {
	if p == nil || len(p.tokens) == 0 {
		return ""
	}

	index := int((atomic.AddUint64(&p.nextIndex, 1) - 1) % uint64(len(p.tokens)))
	p.mu.RLock()
	defer p.mu.RUnlock()

	for offset := range p.tokens {
		token := p.tokens[(index+offset)%len(p.tokens)]
		if p.isPreferredLocked(token) {
			return token
		}
	}

	for offset := range p.tokens {
		token := p.tokens[(index+offset)%len(p.tokens)]
		if !p.isInCooldownLocked(token) {
			return token
		}
	}

	return p.tokens[index]
}

// ObserveTokenRemaining records the latest rate-limit remaining value for a token.
func (p *RoundRobinTokenProvider) ObserveTokenRemaining(token string, remaining int) {
	if p == nil || token == "" || !p.hasToken(token) {
		return
	}
	if remaining < 0 {
		remaining = 0
	}

	p.mu.Lock()
	p.remaining[token] = remaining
	p.mu.Unlock()
}

// ObserveRateLimitedToken marks a token as temporarily unavailable.
func (p *RoundRobinTokenProvider) ObserveRateLimitedToken(token string) {
	if p == nil || token == "" || !p.hasToken(token) {
		return
	}

	p.mu.Lock()
	p.cooldownUntil[token] = p.now().Add(p.cooldownDuration)
	p.mu.Unlock()
}

func (p *RoundRobinTokenProvider) hasToken(token string) bool {
	p.mu.RLock()
	_, ok := p.tokenSet[token]
	p.mu.RUnlock()
	return ok
}

func (p *RoundRobinTokenProvider) isPreferredLocked(token string) bool {
	if p.isInCooldownLocked(token) {
		return false
	}
	if p.threshold <= 0 {
		return true
	}

	remaining, ok := p.remaining[token]
	if !ok {
		return true
	}
	return remaining >= p.threshold
}

func (p *RoundRobinTokenProvider) isInCooldownLocked(token string) bool {
	until, ok := p.cooldownUntil[token]
	if !ok {
		return false
	}
	return p.now().Before(until)
}
