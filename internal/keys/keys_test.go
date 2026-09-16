package keys

import (
	"bytes"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

// testMaster is a 32-byte placeholder master key (the same floor
// config.MinEncryptionKeyBytes enforces) used by every test below.
const testMaster = "0123456789abcdef0123456789abcdef"

// Known-answer vectors (HKDF-SHA256, nil salt, raw master-key bytes as
// input keying material). They pin the derivation so that any accidental
// change to the label, hash, salt handling, or key size breaks the build
// instead of silently invalidating every encrypted database row across a
// binary upgrade.
const (
	katDBEncryptionHex  = "3684ac170cfa0c6909be5d6862939df14b4f149f43b86cd9de5e56b3b48a589a"
	katOtherMasterValue = "fedcba9876543210fedcba9876543210"
)

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("decoding hex %q: %v", s, err)
	}
	return b
}

// TestDeriveDeterministic proves the same master key always yields the
// same secret — the property that keeps ciphertexts valid across restarts
// — via two independent Derive calls and a direct hkdf.Key recomputation.
func TestDeriveDeterministic(t *testing.T) {
	first, err := Derive(testMaster)
	if err != nil {
		t.Fatalf("first Derive: %v", err)
	}
	second, err := Derive(testMaster)
	if err != nil {
		t.Fatalf("second Derive: %v", err)
	}
	if !bytes.Equal(first.DBEncryptionKey, second.DBEncryptionKey) {
		t.Error("database encryption key differs between two derivations of the same master key")
	}

	// Independent path: the package must agree with a bare hkdf.Key call
	// using the documented parameters (SHA-256, nil salt, raw master-key
	// bytes, exported label).
	raw, err := hkdf.Key(sha256.New, []byte(testMaster), nil, LabelDBEncryption, DBEncryptionKeySize)
	if err != nil {
		t.Fatalf("recomputing database encryption key with hkdf.Key: %v", err)
	}
	if !bytes.Equal(first.DBEncryptionKey, raw) {
		t.Errorf("database encryption key %x does not match direct hkdf.Key output %x", first.DBEncryptionKey, raw)
	}
}

// TestDeriveKnownAnswer checks the secret against the pinned vector.
func TestDeriveKnownAnswer(t *testing.T) {
	d, err := Derive(testMaster)
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	if want := mustHex(t, katDBEncryptionHex); !bytes.Equal(d.DBEncryptionKey, want) {
		t.Errorf("database encryption key = %x, want %x", d.DBEncryptionKey, want)
	}
}

// TestDeriveMasterSeparation proves different masters produce unrelated
// keys — the label's domain separation would be worthless if the master
// key did not differentiate outputs.
func TestDeriveMasterSeparation(t *testing.T) {
	a, err := Derive(testMaster)
	if err != nil {
		t.Fatalf("Derive(master A): %v", err)
	}
	b, err := Derive(katOtherMasterValue)
	if err != nil {
		t.Fatalf("Derive(master B): %v", err)
	}
	if bytes.Equal(a.DBEncryptionKey, b.DBEncryptionKey) {
		t.Error("different master keys produced the same database encryption key")
	}
}

// TestDeriveKeySizes checks the documented output size: a 32-byte AES-256
// database key.
func TestDeriveKeySizes(t *testing.T) {
	d, err := Derive(testMaster)
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	if len(d.DBEncryptionKey) != DBEncryptionKeySize {
		t.Errorf("database encryption key size = %d, want %d", len(d.DBEncryptionKey), DBEncryptionKeySize)
	}
}

// TestDeriveRefusesWeakMaster checks the defense-in-depth floor.
func TestDeriveRefusesWeakMaster(t *testing.T) {
	if _, err := Derive("short"); err == nil {
		t.Error("Derive accepted a master key below MinMasterKeyBytes")
	}
}
