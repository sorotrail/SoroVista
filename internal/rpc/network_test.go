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
		{"testnet passphrase", PassphraseTestnet, "testnet"},
		{"mainnet passphrase", PassphraseMainnet, "mainnet"},
		{"futurenet passphrase", PassphraseFuturenet, "futurenet"},
		{"unknown passphrase", "Some Other Network ; Someday", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, NetworkName(tt.passphrase))
		})
	}
}
