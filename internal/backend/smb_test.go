package backend

import (
	"net/url"
	"os"
	"strconv"
	"testing"
	"time"
)

// TestSMB runs against a real SMB server when HERAKLES_TEST_SMB is set, e.g.
// HERAKLES_TEST_SMB=smb://user:pass@127.0.0.1/share
func TestSMB(t *testing.T) {
	base := os.Getenv("HERAKLES_TEST_SMB")
	if base == "" {
		t.Skip("HERAKLES_TEST_SMB not set")
	}
	b, err := NewSMB(base+"/herakles-test-"+strconv.FormatInt(time.Now().UnixNano(), 36), Options{})
	if err != nil {
		t.Fatal(err)
	}
	conformance(t, b)
	u, _ := url.Parse(base)
	u.User = url.UserPassword(u.User.Username(), "definitely-wrong")
	if _, err := NewSMB(u.String(), Options{}); err == nil {
		t.Fatal("wrong SMB password accepted")
	}
}
