package workbench

// AD-4 acceptance at the protocol layer: the value never enters the envelope,
// the lend is one-shot, and the eraser really erases.

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestSecretLendFrameCarriesNoValue(t *testing.T) {
	// The frame's JSON text must contain the NAME and the LENGTH and nothing
	// else. This is the executable form of "秘密不进 JSON 信封": a regression that
	// adds a Value field to SecretLend fails here, not in a review.
	secret := "hunter2-super-secret"
	frame, err := json.Marshal(SecretLend{Name: "ssh.password", Bytes: len(secret)})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(frame), secret) {
		t.Fatalf("the lend frame leaked the value: %s", frame)
	}
	if !strings.Contains(string(frame), "ssh.password") {
		t.Fatalf("the frame must name what is lent: %s", frame)
	}
	if !strings.Contains(string(frame), `"bytes"`) {
		t.Fatalf("the frame must carry the body length: %s", frame)
	}
}

func TestSecretVaultTakeIsOneShotAndErases(t *testing.T) {
	v := NewSecretVault()
	secret := []byte("pa55word")
	if err := v.Lend("ssh.password", secret); err != nil {
		t.Fatal(err)
	}
	// Lend copies: erasing the CALLER's buffer must not affect the vault.
	for i := range secret {
		secret[i] = 'x'
	}
	got, erase, ok := v.Take("ssh.password")
	if !ok {
		t.Fatal("the lent secret was not taken")
	}
	if string(got) != "pa55word" {
		t.Fatalf("the vault did not keep its own copy: %q", got)
	}
	// One-shot: the second take fails.
	if _, _, ok2 := v.Take("ssh.password"); ok2 {
		t.Fatal("a second Take must fail (lends are one-shot)")
	}
	// The eraser zeroes the buffer the caller holds.
	erase()
	for i, b := range got {
		if b != 0 {
			t.Fatalf("byte %d survived the eraser: %q", i, got)
		}
	}
}

func TestSecretVaultReLendErasesTheOldBuffer(t *testing.T) {
	v := NewSecretVault()
	if err := v.Lend("k", []byte("old")); err != nil {
		t.Fatal(err)
	}
	if err := v.Lend("k", []byte("new")); err != nil {
		t.Fatal(err)
	}
	got, erase, ok := v.Take("k")
	if !ok || string(got) != "new" {
		t.Fatalf("re-lend did not replace: %q ok=%v", got, ok)
	}
	erase()
}

func TestSecretVaultRefusesEmptyAndOversized(t *testing.T) {
	v := NewSecretVault()
	if err := v.Lend("", []byte("x")); err == nil {
		t.Fatal("an empty name must be refused")
	}
	if err := v.Lend("k", nil); err == nil {
		t.Fatal("an empty secret must be refused")
	}
	if err := v.Lend("k", make([]byte, MaxSecretBytes+1)); err == nil {
		t.Fatal("an oversized secret must be refused")
	}
}

func TestReadSecretBody(t *testing.T) {
	body := []byte("s3cret!")
	r := bytes.NewReader(append(append([]byte{}, body...), []byte("\n{\"id\":1}\n")...))
	got, err := ReadSecretBody(r, len(body))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("body = %q", got)
	}
	// The reader is positioned right after the body: the next line is a line.
	rest := make([]byte, 16)
	n, _ := r.Read(rest)
	if !strings.HasPrefix(string(rest[:n]), "\n{") {
		t.Fatalf("the reader is not positioned after the body: %q", rest[:n])
	}
	// A short body is an error, not a partial password.
	if _, err := ReadSecretBody(bytes.NewReader([]byte("ab")), 5); err == nil {
		t.Fatal("a truncated body must be refused")
	}
	// An absurd length is refused before allocating.
	if _, err := ReadSecretBody(bytes.NewReader(nil), MaxSecretBytes+1); err == nil {
		t.Fatal("an oversized body must be refused")
	}
}

func TestZeroAll(t *testing.T) {
	v := NewSecretVault()
	_ = v.Lend("a", []byte("one"))
	_ = v.Lend("b", []byte("two"))
	v.ZeroAll()
	if v.Has("a") || v.Has("b") {
		t.Fatal("ZeroAll left a lend behind")
	}
}
