package cooldown

import (
	"time"
)

// CooldownPolicy defines the interface for cooldown calculation strategies
type CooldownPolicy interface {
	// CalculateCooldown calculates cooldown duration based on failure count
	CalculateCooldown(failureCount int) time.Duration
}

// FixedDurationPolicy returns a fixed cooldown duration regardless of failure count
type FixedDurationPolicy struct {
	Duration time.Duration
}

func (p *FixedDurationPolicy) CalculateCooldown(failureCount int) time.Duration {
	return p.Duration
}

// LinearIncrementalPolicy increases cooldown linearly with each failure
// Formula: baseMinutes * failureCount
type LinearIncrementalPolicy struct {
	BaseMinutes int
	MaxMinutes  int // Optional cap, 0 means no limit
}

func (p *LinearIncrementalPolicy) CalculateCooldown(failureCount int) time.Duration {
	minutes := p.BaseMinutes * failureCount
	if p.MaxMinutes > 0 && minutes > p.MaxMinutes {
		minutes = p.MaxMinutes
	}
	return time.Duration(minutes) * time.Minute
}

// ExponentialBackoffPolicy increases cooldown exponentially with each failure
// Formula: baseMinutes * (2 ^ (failureCount - 1))
type ExponentialBackoffPolicy struct {
	BaseMinutes int
	MaxMinutes  int // Optional cap, 0 means no limit
}

func (p *ExponentialBackoffPolicy) CalculateCooldown(failureCount int) time.Duration {
	if failureCount == 0 {
		return 0
	}

	minutes := p.BaseMinutes
	for i := 1; i < failureCount; i++ {
		minutes *= 2
		if p.MaxMinutes > 0 && minutes > p.MaxMinutes {
			minutes = p.MaxMinutes
			break
		}
	}

	return time.Duration(minutes) * time.Minute
}

// CooldownReason represents the reason for cooldown
type CooldownReason string

const (
	ReasonServerError     CooldownReason = "server_error"          // 5xx errors
	ReasonNetworkError    CooldownReason = "network_error"         // Connection timeout, DNS failure, etc.
	ReasonQuotaExhausted  CooldownReason = "quota_exhausted"       // API quota exhausted (fallback when no explicit time)
	ReasonRateLimit       CooldownReason = "rate_limit_exceeded"   // Rate limit (fallback when no explicit time)
	ReasonConcurrentLimit CooldownReason = "concurrent_limit"      // Concurrent request limit (fallback when no explicit time)
	ReasonUnknown         CooldownReason = "unknown"               // Unknown error
)

// DefaultPolicies returns the default policy configuration
// Note: For quota/rate limit errors with explicit reset times from API,
// those times will be used directly instead of these policies
func DefaultPolicies() map[CooldownReason]CooldownPolicy {
	return map[CooldownReason]CooldownPolicy{
		// Server errors (5xx): linear increment (1min, 2min, 3min, ... max 10min)
		ReasonServerError: &LinearIncrementalPolicy{
			BaseMinutes: 1,
			MaxMinutes:  10,
		},
		// Network errors: exponential backoff (1min, 2min, 4min, 8min, ... max 30min)
		ReasonNetworkError: &ExponentialBackoffPolicy{
			BaseMinutes: 1,
			MaxMinutes:  30,
		},
		// Quota exhausted: fixed 1 hour (only used as fallback when API doesn't return reset time)
		ReasonQuotaExhausted: &FixedDurationPolicy{
			Duration: 1 * time.Hour,
		},
		// Rate limit: fixed 1 minute (only used as fallback when API doesn't return Retry-After)
		ReasonRateLimit: &FixedDurationPolicy{
			Duration: 1 * time.Minute,
		},
		// Concurrent limit: fixed 10 seconds (only used as fallback)
		ReasonConcurrentLimit: &FixedDurationPolicy{
			Duration: 10 * time.Second,
		},
		// Unknown error: linear increment (1min, 2min, 3min, ... max 5min)
		ReasonUnknown: &LinearIncrementalPolicy{
			BaseMinutes: 1,
			MaxMinutes:  5,
		},
	}
}
