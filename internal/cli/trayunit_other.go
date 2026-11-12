//go:build !linux

package cli

func inTrayUnit() bool { return false }

func (a *App) startTrayUnit() bool { return false }
