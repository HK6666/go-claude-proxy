package antigravity

import "sync"

// Global thought_signature storage (like Antigravity-Manager's signature_store.rs).
// Used as a last-resort fallback when clients strip thoughtSignature in tool loops.
var globalThoughtSignatureStore struct {
	mu  sync.Mutex
	sig string
}

// StoreThoughtSignature stores the signature if it's longer than the existing one.
func StoreThoughtSignature(sig string) {
	if sig == "" {
		return
	}

	globalThoughtSignatureStore.mu.Lock()
	defer globalThoughtSignatureStore.mu.Unlock()

	if globalThoughtSignatureStore.sig == "" || len(sig) > len(globalThoughtSignatureStore.sig) {
		globalThoughtSignatureStore.sig = sig
	}
}

// GetThoughtSignature returns the currently stored signature (or "" if none).
func GetThoughtSignature() string {
	globalThoughtSignatureStore.mu.Lock()
	defer globalThoughtSignatureStore.mu.Unlock()
	return globalThoughtSignatureStore.sig
}

// ClearThoughtSignature clears the stored signature (mainly for tests).
func ClearThoughtSignature() {
	globalThoughtSignatureStore.mu.Lock()
	defer globalThoughtSignatureStore.mu.Unlock()
	globalThoughtSignatureStore.sig = ""
}
