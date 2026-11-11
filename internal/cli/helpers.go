package cli

import "github.com/lamisator/herakles/internal/crypto"

func cryptoGenerateKeyFile(p string) error { return crypto.GenerateKeyFile(p) }
