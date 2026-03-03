package antigravity

import (
	"strings"
	"sync"
	"time"
)

// SignatureCache provides a two-layer signature cache (like Antigravity-Manager):
// 1) tool_use_id -> thought signature
// 2) thought signature -> model family string
type SignatureCache struct {
	mu sync.Mutex

	// Layer 1: Tool Use ID -> Thinking Signature
	toolSignatures map[string]signatureCacheEntry

	// Layer 2: Thinking Signature -> Model Family
	thinkingFamilies map[string]signatureCacheEntry
}

type signatureCacheEntry struct {
	data      string
	timestamp time.Time
}

const (
	// SignatureCacheTTL follows Antigravity-Manager (2 hours)
	SignatureCacheTTL = 2 * time.Hour

	// MinSignatureLength is the minimum length for a valid thought signature
	// [Aligned with Antigravity-Manager/src-tauri/src/proxy/signature_cache.rs]
	MinSignatureLength = 50

	// MinThinkingSignatureLength is the minimum length treated as a "valid" thinking signature
	// when filtering/cleaning Claude history.
	// [Aligned with Antigravity-Manager/src-tauri/src/proxy/handlers/claude.rs]
	MinThinkingSignatureLength = 10

	// signatureCacheMaxEntries matches Antigravity-Manager's simple cleanup strategy.
	signatureCacheMaxEntries = 1000
)

func newSignatureCache() *SignatureCache {
	return &SignatureCache{
		toolSignatures:   make(map[string]signatureCacheEntry),
		thinkingFamilies: make(map[string]signatureCacheEntry),
	}
}

var globalSignatureCache = newSignatureCache()

// GlobalSignatureCache returns the global signature cache instance
func GlobalSignatureCache() *SignatureCache {
	return globalSignatureCache
}

func (e signatureCacheEntry) expired(now time.Time) bool {
	return now.Sub(e.timestamp) > SignatureCacheTTL
}

// HasValidSignature checks if a signature is valid (non-empty and long enough)
// (like CLIProxyAPI's HasValidSignature)
func HasValidSignature(signature string) bool {
	return signature != "" && len(signature) >= MinSignatureLength
}

// CacheToolSignature stores a signature for a specific tool call ID (Layer 1).
func (c *SignatureCache) CacheToolSignature(toolID, signature string) {
	if signature == "" || len(signature) < MinSignatureLength {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	c.toolSignatures[toolID] = signatureCacheEntry{data: signature, timestamp: now}

	if len(c.toolSignatures) > signatureCacheMaxEntries {
		for key, entry := range c.toolSignatures {
			if entry.expired(now) {
				delete(c.toolSignatures, key)
			}
		}
	}
}

// GetToolSignature retrieves a cached signature for a tool call ID
func (c *SignatureCache) GetToolSignature(toolID string) string {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, ok := c.toolSignatures[toolID]
	if !ok {
		return ""
	}
	now := time.Now()
	if entry.expired(now) {
		delete(c.toolSignatures, toolID)
		return ""
	}
	return entry.data
}

// CacheThinkingFamily stores model family for a signature (Layer 2).
func (c *SignatureCache) CacheThinkingFamily(signature, family string) {
	if signature == "" || len(signature) < MinSignatureLength {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	c.thinkingFamilies[signature] = signatureCacheEntry{data: family, timestamp: now}

	if len(c.thinkingFamilies) > signatureCacheMaxEntries {
		for key, entry := range c.thinkingFamilies {
			if entry.expired(now) {
				delete(c.thinkingFamilies, key)
			}
		}
	}
}

// GetSignatureFamily returns the model family that generated a given signature
func (c *SignatureCache) GetSignatureFamily(signature string) string {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, ok := c.thinkingFamilies[signature]
	if !ok {
		return ""
	}
	now := time.Now()
	if entry.expired(now) {
		delete(c.thinkingFamilies, signature)
		return ""
	}
	return entry.data
}

// Clear clears all caches (for tests or manual reset).
func (c *SignatureCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.toolSignatures = make(map[string]signatureCacheEntry)
	c.thinkingFamilies = make(map[string]signatureCacheEntry)
}

// IsModelCompatible checks if two models are compatible (same family)
func IsModelCompatible(cached, target string) bool {
	c := strings.ToLower(cached)
	t := strings.ToLower(target)

	if c == t {
		return true
	}

	// Check specific families
	if strings.Contains(c, "gemini-1.5") && strings.Contains(t, "gemini-1.5") {
		return true
	}
	if strings.Contains(c, "gemini-2.0") && strings.Contains(t, "gemini-2.0") {
		return true
	}
	if strings.Contains(c, "claude-3-5") && strings.Contains(t, "claude-3-5") {
		return true
	}
	if strings.Contains(c, "claude-3-7") && strings.Contains(t, "claude-3-7") {
		return true
	}

	// Fallback: strict match required
	return false
}
