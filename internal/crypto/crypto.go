// Package crypto implements authenticated encryption (AES-256-GCM), keyed
// content hashing (HMAC-SHA256) and password/key-file based key derivation
// (Argon2id) for Herakles repositories.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"sync"

	"github.com/lamisator/herakles/internal/id"
)

// KeySize is the size of an AES-256 key in bytes.
const KeySize = 32

// NonceSize is the size of the GCM nonce.
const NonceSize = 12

// Overhead is the number of bytes Seal adds to a plaintext.
const Overhead = NonceSize + 16

// ErrUnauthenticated is returned when a ciphertext fails authentication.
var ErrUnauthenticated = errors.New("ciphertext verification failed (wrong key or corrupted data)")

// Key is the repository master key. It never leaves the client unencrypted.
type Key struct {
	EncryptionKey []byte `json:"encrypt"`
	MACKey        []byte `json:"mac"`
	ChunkerSeed   uint64 `json:"chunker_seed"`

	aead    cipher.AEAD
	macPool sync.Pool
}

// NewRandomKey generates a fresh random master key.
func NewRandomKey() *Key {
	k := &Key{EncryptionKey: make([]byte, KeySize), MACKey: make([]byte, KeySize)}
	mustRand(k.EncryptionKey)
	mustRand(k.MACKey)
	var seed [8]byte
	mustRand(seed[:])
	k.ChunkerSeed = binary.LittleEndian.Uint64(seed[:])
	if err := k.Init(); err != nil {
		panic(err)
	}
	return k
}

// Init prepares internal state; it must be called after unmarshalling.
func (k *Key) Init() error {
	if len(k.EncryptionKey) != KeySize || len(k.MACKey) != KeySize {
		return errors.New("invalid master key length")
	}
	aead, err := newGCM(k.EncryptionKey)
	if err != nil {
		return err
	}
	k.aead = aead
	mac := k.MACKey
	k.macPool.New = func() any { return hmac.New(sha256.New, mac) }
	return nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func mustRand(b []byte) {
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
}

// Seal encrypts and authenticates plaintext. The result is nonce||ciphertext||tag.
func (k *Key) Seal(plaintext []byte) []byte {
	out := make([]byte, NonceSize, NonceSize+len(plaintext)+k.aead.Overhead())
	mustRand(out)
	return k.aead.Seal(out, out[:NonceSize], plaintext, nil)
}

// Open verifies and decrypts a ciphertext produced by Seal.
func (k *Key) Open(ciphertext []byte) ([]byte, error) {
	if len(ciphertext) < Overhead {
		return nil, ErrUnauthenticated
	}
	pt, err := k.aead.Open(nil, ciphertext[:NonceSize], ciphertext[NonceSize:], nil)
	if err != nil {
		return nil, ErrUnauthenticated
	}
	return pt, nil
}

// ID computes the keyed content identifier of data. Because the hash is keyed
// with a secret, blob IDs do not leak information about file contents.
func (k *Key) ID(data []byte) id.ID {
	h := k.macPool.Get().(hash.Hash)
	h.Reset()
	h.Write(data)
	var out id.ID
	h.Sum(out[:0])
	k.macPool.Put(h)
	return out
}
