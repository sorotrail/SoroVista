package rpc

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNetworkName(t *testing.T) {
	tests := []struct {
		name       string
		passphrase string
		want       string
	}{
		{"testnet", PassphraseTestnet, "testnet"},
		{"mainnet", PassphraseMainnet, "mainnet"},
		{"futurenet", PassphraseFuturenet, "futurenet"},
		// Anything else is a standalone/private network; callers use the empty
		// string to mean "not a known preset" rather than a matching failure.
		{"unknown passphrase", "My Standalone Network ; November 2026", ""},
		{"empty passphrase", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, NetworkName(tt.passphrase))
		})
	}
}
