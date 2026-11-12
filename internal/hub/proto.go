package hub

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The signed messages of the protocol. Each starts with its own label, so
// a signature for one step is useless in another.
//
//	enrollment  server → hub   POST /hub/join {secret, public key, nonce}
//	            hub → server   {id, name, sign_hub(join label, nonce, key, id, name)}
//	connection  server → hub   GET /hub/connect (websocket) with
//	                           X-Herakles-Auth: id.time.nonce.sign_server(connect label, ...)
//	            hub → server   X-Herakles-Hub-Sig: sign_hub(hub label, id, nonce)
func joinMessage(nonce []byte, pub ed25519.PublicKey, id, name string) []byte {
	return join0("herakles-join-v1", string(nonce), string(pub), id, name)
}

func connectMessage(id string, ts int64, nonce string, hubKey ed25519.PublicKey) []byte {
	return join0("herakles-connect-v1", id, strconv.FormatInt(ts, 10), nonce, string(hubKey))
}

func hubMessage(id, nonce string) []byte {
	return join0("herakles-hub-v1", id, nonce)
}

func join0(parts ...string) []byte { return []byte(strings.Join(parts, "\x00")) }

// Headers of the connection handshake.
const (
	AuthHeader   = "X-Herakles-Auth"
	HubSigHeader = "X-Herakles-Hub-Sig"
)

// MaxClockSkew is how far a server's clock may be off from the hub's.
const MaxClockSkew = 5 * time.Minute

var b64 = base64.RawURLEncoding

// connectAuth builds the X-Herakles-Auth value of a server.
func connectAuth(id string, key ed25519.PrivateKey, hubKey ed25519.PublicKey, now time.Time) (header, nonce string) {
	nonce = randomHex(16)
	ts := now.Unix()
	sig := ed25519.Sign(key, connectMessage(id, ts, nonce, hubKey))
	return id + "." + strconv.FormatInt(ts, 10) + "." + nonce + "." + b64.EncodeToString(sig), nonce
}

type parsedAuth struct {
	id, nonce string
	ts        int64
	sig       []byte
}

func parseAuth(h string) (parsedAuth, error) {
	p := strings.Split(h, ".")
	if len(p) != 4 {
		return parsedAuth{}, errors.New("invalid login")
	}
	ts, err := strconv.ParseInt(p[1], 10, 64)
	if err != nil {
		return parsedAuth{}, errors.New("invalid login")
	}
	sig, err := b64.DecodeString(p[3])
	if err != nil || len(sig) != ed25519.SignatureSize || len(p[2]) != 32 {
		return parsedAuth{}, errors.New("invalid login")
	}
	return parsedAuth{id: p[0], ts: ts, nonce: p[2], sig: sig}, nil
}

// replayGuard remembers recent login nonces so a recorded login cannot be
// used again.
type replayGuard struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

func (g *replayGuard) fresh(nonce string, now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.seen == nil {
		g.seen = map[string]time.Time{}
	}
	for k, t := range g.seen {
		if now.Sub(t) > 2*MaxClockSkew {
			delete(g.seen, k)
		}
	}
	if _, ok := g.seen[nonce]; ok {
		return false
	}
	g.seen[nonce] = now
	return true
}
