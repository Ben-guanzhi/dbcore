package workbench

// vault.go — AD-4's plugin-side half: the lent-secret vault.
//
// The discipline the plan asks for, made structural:
//
//   - A secret ENTERS only as a raw stdin body (ReadSecretBody), never as a JSON
//     field. The frame carries a name and a length; the value is the bytes.
//   - A secret LEAVES only through Take, which is ONE-SHOT and hands back an
//     eraser: the caller must zero its copy after use. There is no "read the
//     secret" accessor — you cannot hold a lent secret without also holding the
//     duty to erase it (the type-forced form of §85.1's four-path rule).
//   - ZeroAll runs on plugin shutdown: whatever the plugin failed to consume is
//     erased at the process boundary rather than left for the OS to recycle.
//
// The vault lives in the PLUGIN's memory only. It never touches disk, never
// enters the key surface, and never appears in a log line.

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sync"
)

// Zero overwrites b in place. Used by every eraser; kept exported so a caller
// holding a copy can erase it too.
func Zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// ReadSecretBody reads exactly n raw bytes — the body that follows a SecretLend
// control line. It refuses an absurd length and a short body: a truncated secret
// must not be handed to an authenticator as if it were the password.
func ReadSecretBody(r io.Reader, n int) ([]byte, error) {
	if n < 0 || n > MaxSecretBytes {
		return nil, fmt.Errorf("%s: secret body length %d out of range", CodeBadBanner, n)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		Zero(buf)
		return nil, fmt.Errorf("secret body: %w", err)
	}
	return buf, nil
}

// SecretVault holds lent secrets by name. Safe for concurrent use: the plugin's
// action handlers may race with the line loop that lends.
type SecretVault struct {
	mu sync.Mutex
	m  map[string][]byte
}

// NewSecretVault returns an empty vault.
func NewSecretVault() *SecretVault {
	return &SecretVault{m: map[string][]byte{}}
}

// Lend stores a copy of value under name, replacing any previous lend of the
// same name (the old buffer is erased first — a re-lend must not leave the old
// secret readable).
func (v *SecretVault) Lend(name string, value []byte) error {
	if name == "" {
		return errors.New("secret name must not be empty")
	}
	if len(value) == 0 {
		return errors.New("lent secret is empty")
	}
	if len(value) > MaxSecretBytes {
		return fmt.Errorf("secret %s exceeds %d bytes", name, MaxSecretBytes)
	}
	cp := make([]byte, len(value))
	copy(cp, value)
	v.mu.Lock()
	if old, ok := v.m[name]; ok {
		Zero(old)
	}
	v.m[name] = cp
	v.mu.Unlock()
	return nil
}

// Take removes and returns the secret, with the eraser the caller must run after
// use. The second return value reports whether the name was lent at all: a
// second Take of the same name fails, which is what makes a lend ONE-SHOT.
func (v *SecretVault) Take(name string) ([]byte, func(), bool) {
	v.mu.Lock()
	val, ok := v.m[name]
	delete(v.m, name)
	v.mu.Unlock()
	if !ok {
		return nil, nil, false
	}
	eraser := func() { Zero(val) }
	return val, eraser, true
}

// Has reports whether name is currently lent (used to answer "why did the open
// fail" with the precise cause).
func (v *SecretVault) Has(name string) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	_, ok := v.m[name]
	return ok
}

// ZeroAll erases every remaining lend (plugin shutdown).
func (v *SecretVault) ZeroAll() {
	v.mu.Lock()
	defer v.mu.Unlock()
	for name, val := range v.m {
		Zero(val)
		delete(v.m, name)
	}
}

// EncodeUint32 is a tiny helper for tests that hand-build a lend body length.
func EncodeUint32(n uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, n)
	return b
}
