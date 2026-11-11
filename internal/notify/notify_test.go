package notify

import (
	"bufio"
	"context"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/lamisator/herakles/internal/config"
)

func TestWants(t *testing.T) {
	ev := Event{Job: "a", Status: "failure"}
	if !wants(&config.Notification{}, ev) {
		t.Fatal("default should include failure")
	}
	if wants(&config.Notification{}, Event{Job: "a", Status: "success"}) {
		t.Fatal("default should exclude success")
	}
	if wants(&config.Notification{Jobs: []string{"b"}}, ev) {
		t.Fatal("job filter ignored")
	}
}

func TestCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip()
	}
	out := filepath.Join(t.TempDir(), "out")
	err := Send(context.Background(), []*config.Notification{{Type: "command", Command: "cat > " + out + "; echo $HERAKLES_STATUS >> " + out, On: []string{"success"}}},
		Event{Job: "j", Status: "success", Start: time.Now(), End: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(out)
	if !strings.Contains(string(data), `"job":"j"`) || !strings.HasSuffix(strings.TrimSpace(string(data)), "success") {
		t.Fatalf("command got %q", data)
	}
}

// fakeSMTP accepts one message and returns its DATA section.
func fakeSMTP(t *testing.T) (string, <-chan string) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ch := make(chan string, 1)
	go func() {
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		r := bufio.NewReader(c)
		w := func(s string) { c.Write([]byte(s + "\r\n")) }
		w("220 fake")
		var data strings.Builder
		inData := false
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			if inData {
				if line == ".\r\n" {
					inData = false
					w("250 ok")
					ch <- data.String()
					continue
				}
				data.WriteString(line)
				continue
			}
			switch cmd := strings.ToUpper(strings.TrimSpace(line)); {
			case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
				w("250 hello")
			case cmd == "DATA":
				inData = true
				w("354 go")
			case cmd == "QUIT":
				w("221 bye")
				return
			default:
				w("250 ok")
			}
		}
	}()
	return l.Addr().String(), ch
}

func TestEmail(t *testing.T) {
	addr, ch := fakeSMTP(t)
	host, port, _ := net.SplitHostPort(addr)
	var p int
	for _, c := range port {
		p = p*10 + int(c-'0')
	}
	n := &config.Notification{Type: "email", SMTPHost: host, SMTPPort: p, From: "a@b", To: []string{"c@d"}}
	if err := Send(context.Background(), []*config.Notification{n}, Event{Job: "home", Status: "failure", Error: "disk full", Hostname: "h"}); err != nil {
		t.Fatal(err)
	}
	msg := <-ch
	if !strings.Contains(msg, "FAILURE: job home") || !strings.Contains(msg, "disk full") {
		t.Fatalf("mail: %q", msg)
	}
}
