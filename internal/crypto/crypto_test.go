package crypto

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func init() { DefaultKDFParams = KDFParams{Time: 1, Memory: 1024, Threads: 1} }

func TestSealOpen(t *testing.T) {
	k := NewRandomKey()
	msg := []byte("hello herakles")
	ct := k.Seal(msg)
	if bytes.Contains(ct, msg) {
		t.Fatal("ciphertext contains plaintext")
	}
	pt, err := k.Open(ct)
	if err != nil || !bytes.Equal(pt, msg) {
		t.Fatalf("roundtrip failed: %v", err)
	}
	ct[len(ct)-1] ^= 1
	if _, err := k.Open(ct); err == nil {
		t.Fatal("tampered ciphertext accepted")
	}
	if _, err := k.Open([]byte{1, 2}); err == nil {
		t.Fatal("short ciphertext accepted")
	}
	// Nonces must differ.
	if bytes.Equal(k.Seal(msg), k.Seal(msg)) {
		t.Fatal("identical ciphertexts for repeated Seal")
	}
}

func TestKeyedID(t *testing.T) {
	a, b := NewRandomKey(), NewRandomKey()
	d := []byte("data")
	if a.ID(d) != a.ID(d) {
		t.Fatal("ID not deterministic")
	}
	if a.ID(d) == b.ID(d) {
		t.Fatal("IDs of different keys must differ")
	}
}

func TestWrapUnwrap(t *testing.T) {
	dir := t.TempDir()
	kf := filepath.Join(dir, "key")
	if err := GenerateKeyFile(kf); err != nil {
		t.Fatal(err)
	}
	if err := GenerateKeyFile(kf); err == nil {
		t.Fatal("GenerateKeyFile overwrote existing file")
	}
	kfData, err := LoadKeyFile(kf)
	if err != nil {
		t.Fatal(err)
	}
	master := NewRandomKey()
	cases := []Credentials{
		{Password: "secret"},
		{KeyFileData: kfData},
		{Password: "secret", KeyFileData: kfData},
	}
	for _, c := range cases {
		env, err := Wrap(master, c, "t")
		if err != nil {
			t.Fatal(err)
		}
		k, err := env.Unwrap(c)
		if err != nil {
			t.Fatalf("%s: %v", env.Describe(), err)
		}
		if !bytes.Equal(k.EncryptionKey, master.EncryptionKey) || k.ChunkerSeed != master.ChunkerSeed {
			t.Fatal("unwrapped key differs")
		}
		bad := c
		bad.Password += "x"
		if c.Password == "" {
			bad.Password = ""
			bad.KeyFileData = append([]byte{1}, kfData...)
		}
		if _, err := env.Unwrap(bad); err == nil {
			t.Fatalf("%s: wrong credentials accepted", env.Describe())
		}
	}
	if _, err := Wrap(master, Credentials{}, ""); err == nil {
		t.Fatal("empty credentials accepted")
	}
	os.WriteFile(filepath.Join(dir, "short"), []byte("x"), 0o600)
	if _, err := LoadKeyFile(filepath.Join(dir, "short")); err == nil {
		t.Fatal("short key file accepted")
	}
}
