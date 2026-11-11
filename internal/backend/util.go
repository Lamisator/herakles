package backend

import (
	"crypto/rand"
	"encoding/hex"
)

func randomSuffix() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
