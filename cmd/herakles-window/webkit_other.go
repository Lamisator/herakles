//go:build !linux || !(amd64 || arm64)

package main

import "errors"

func run(url, title, icon string, w, h int) error {
	return errors.New("not supported on this system")
}
