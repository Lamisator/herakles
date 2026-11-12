// Command herakles-window shows the Herakles GUI in a window of its own on
// Linux, using the system's WebKitGTK (libwebkit2gtk-4.1) instead of a web
// browser. It is a separate program because loading GTK makes a binary
// depend on the C library, and herakles itself stays a static binary that
// runs anywhere:
//
//	herakles-window [--title T] [--size WxH] [--icon NAME] [URL]
//
// Without URL it shows $HERAKLES_WINDOW_URL: unlike the command line, the
// environment (with the GUI's session token) is not readable by other
// users.
//
// Exit code 2 means the libraries or a display are missing: herakles then
// opens a browser instead.
package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	title := flag.String("title", "Herakles", "window title")
	size := flag.String("size", "1280x860", "window size")
	icon := flag.String("icon", "herakles", "icon name from the icon theme")
	flag.Parse()
	url := os.Getenv("HERAKLES_WINDOW_URL")
	if flag.NArg() == 1 {
		url = flag.Arg(0)
	}
	if url == "" || flag.NArg() > 1 {
		fmt.Fprintln(os.Stderr, "usage: herakles-window [--title T] [--size WxH] [--icon NAME] [URL]")
		os.Exit(1)
	}
	var w, h int
	if _, err := fmt.Sscanf(*size, "%dx%d", &w, &h); err != nil || w <= 0 || h <= 0 {
		w, h = 1280, 860
	}
	if err := run(url, *title, *icon, w, h); err != nil {
		fmt.Fprintln(os.Stderr, "herakles-window:", err)
		os.Exit(2)
	}
}
