// Package id provides the 256-bit content identifier used throughout Herakles.
package id

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// Size is the length of an ID in bytes.
const Size = 32

// ID identifies a blob, pack, snapshot or other repository object.
type ID [Size]byte

// Null is the zero ID.
var Null ID

// Hash returns the (unkeyed) SHA-256 of data. It is used for pack and
// repository-file names, never for identifying plaintext content.
func Hash(data []byte) ID { return ID(sha256.Sum256(data)) }

// Random returns a random ID.
func Random() ID {
	var i ID
	if _, err := rand.Read(i[:]); err != nil {
		panic(err)
	}
	return i
}

// String returns the hex representation of the ID.
func (i ID) String() string { return hex.EncodeToString(i[:]) }

// Short returns the first 8 hex characters of the ID.
func (i ID) Short() string { return hex.EncodeToString(i[:4]) }

// IsNull reports whether the ID is the zero ID.
func (i ID) IsNull() bool { return i == Null }

// Parse parses a full-length hex ID.
func Parse(s string) (ID, error) {
	var i ID
	if len(s) != 2*Size {
		return i, fmt.Errorf("invalid id %q: wrong length", s)
	}
	if _, err := hex.Decode(i[:], []byte(s)); err != nil {
		return i, fmt.Errorf("invalid id %q: %w", s, err)
	}
	return i, nil
}

// MarshalText implements encoding.TextMarshaler.
func (i ID) MarshalText() ([]byte, error) { return []byte(i.String()), nil }

// UnmarshalText implements encoding.TextUnmarshaler.
func (i *ID) UnmarshalText(b []byte) error {
	p, err := Parse(string(b))
	if err != nil {
		return err
	}
	*i = p
	return nil
}

// Set is a set of IDs.
type Set map[ID]struct{}

// NewSet creates a set containing ids.
func NewSet(ids ...ID) Set {
	s := make(Set, len(ids))
	for _, i := range ids {
		s[i] = struct{}{}
	}
	return s
}

// Has reports whether i is in the set.
func (s Set) Has(i ID) bool { _, ok := s[i]; return ok }

// Insert adds i to the set.
func (s Set) Insert(i ID) { s[i] = struct{}{} }

// Delete removes i from the set.
func (s Set) Delete(i ID) { delete(s, i) }
