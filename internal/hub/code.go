package hub

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"hash/crc32"
	"net/url"
	"os"
	"strings"
	"time"
)

// CodePrefix starts every enrollment code (version 1).
const CodePrefix = "HKE1-"

// Code is everything a server needs to attach to a hub, in one string.
type Code struct {
	// URL is the hub's web address.
	URL string `json:"u"`
	// Secret is the one-time enrollment secret.
	Secret string `json:"s"`
	// HubKey is the hub's public identity key: the server accepts only
	// answers signed with it.
	HubKey []byte `json:"k"`
	// TLSPin is the SHA-256 of the public key of the hub's own TLS
	// certificate (web.tls_cert), so a self-signed certificate works
	// without a CA file. Empty behind a reverse proxy.
	TLSPin []byte `json:"p,omitempty"`
	// Name is the name the server gets on the hub (empty: its host name).
	Name string `json:"n,omitempty"`
	// Expires is when the secret stops working (Unix seconds).
	Expires int64 `json:"e"`
}

// Encode returns the code as text: prefix, base64url(JSON + CRC-32).
func (c Code) Encode() string {
	b, _ := json.Marshal(c)
	b = binary.BigEndian.AppendUint32(b, crc32.ChecksumIEEE(b))
	return CodePrefix + base64.RawURLEncoding.EncodeToString(b)
}

// ParseCode reads an enrollment code.
func ParseCode(s string) (*Code, error) {
	s = strings.Join(strings.Fields(s), "")
	if !strings.HasPrefix(s, CodePrefix) {
		return nil, errors.New("not a Herakles enrollment code (they start with " + CodePrefix + ")")
	}
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(s, CodePrefix))
	if err != nil || len(b) < 5 {
		return nil, errors.New("the enrollment code is damaged (copy it again, completely)")
	}
	body, sum := b[:len(b)-4], binary.BigEndian.Uint32(b[len(b)-4:])
	if crc32.ChecksumIEEE(body) != sum {
		return nil, errors.New("the enrollment code is damaged (checksum mismatch; copy it again, completely)")
	}
	var c Code
	if err := json.Unmarshal(body, &c); err != nil {
		return nil, fmt.Errorf("the enrollment code is damaged: %w", err)
	}
	u, err := url.Parse(c.URL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, fmt.Errorf("the enrollment code has no valid hub address (%q)", c.URL)
	}
	if len(c.HubKey) != ed25519.PublicKeySize || c.Secret == "" {
		return nil, errors.New("the enrollment code is incomplete")
	}
	if c.TLSPin != nil && len(c.TLSPin) != sha256.Size {
		return nil, errors.New("the enrollment code has an invalid certificate pin")
	}
	return &c, nil
}

// ExpiresAt is the expiry as time.
func (c Code) ExpiresAt() time.Time { return time.Unix(c.Expires, 0) }

// KeyFingerprint shows a public key as 16 hex groups (for humans).
func KeyFingerprint(k []byte) string {
	h := sha256.Sum256(k)
	s := hex.EncodeToString(h[:16])
	var parts []string
	for i := 0; i < len(s); i += 4 {
		parts = append(parts, s[i:i+4])
	}
	return strings.Join(parts, ":")
}

// CertPin returns the SHA-256 of a PEM certificate's public key (nil for
// an empty path).
func CertPin(certFile string) ([]byte, error) {
	if certFile == "" {
		return nil, nil
	}
	data, err := os.ReadFile(certFile)
	if err != nil {
		return nil, err
	}
	blk, _ := pem.Decode(data)
	if blk == nil {
		return nil, fmt.Errorf("%s: no PEM certificate", certFile)
	}
	cert, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		return nil, err
	}
	h := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return h[:], nil
}
