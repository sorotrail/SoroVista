package config

import (
	"strings"
	"testing"

	"github.com/sorotrail/sorovista/internal/rpc"
)

func TestParseNetwork(t *testing.T) {
	tests := []struct {
		name       string
		env        map[string]string
		wantName   string
		wantRPC    string
		wantPhrase string
		wantErr    string
	}{
		{"unset defaults to testnet", map[string]string{}, "testnet",
			"https://soroban-testnet.stellar.org", rpc.PassphraseTestnet, ""},
		{"mainnet preset", map[string]string{"NETWORK": "mainnet"}, "mainnet",
			"https://soroban-mainnet.stellar.org", rpc.PassphraseMainnet, ""},
		{"futurenet preset", map[string]string{"NETWORK": "futurenet"}, "futurenet",
			"https://rpc-futurenet.stellar.org", rpc.PassphraseFuturenet, ""},
		{"custom rpc override keeps preset passphrase",
			map[string]string{"NETWORK": "testnet", "RPC_URL": "http://localhost:8000"},
			"testnet", "http://localhost:8000", rpc.PassphraseTestnet, ""},
		{"custom passphrase override",
			map[string]string{"NETWORK": "testnet", "NETWORK_PASSPHRASE": "Standalone Network ; February 7th 1974 at 8:37:19 PM"},
			"testnet", "https://soroban-testnet.stellar.org",
			"Standalone Network ; February 7th 1974 at 8:37:19 PM", ""},
		{"custom network with both set",
			map[string]string{"NETWORK": "custom", "RPC_URL": "http://localhost:8000", "NETWORK_PASSPHRASE": "p"},
			"custom", "http://localhost:8000", "p", ""},
		{"custom without rpc url",
			map[string]string{"NETWORK": "custom", "NETWORK_PASSPHRASE": "p"}, "", "", "", "RPC_URL is required"},
		{"custom without passphrase",
			map[string]string{"NETWORK": "custom", "RPC_URL": "http://localhost:8000"}, "", "", "", "NETWORK_PASSPHRASE is required"},
		{"unknown network", map[string]string{"NETWORK": "regtest"}, "", "", "", "invalid NETWORK"},
		// Edge cases: input normalization and explicitly empty overrides.
		{"network name is trimmed and lowercased", map[string]string{"NETWORK": "  MaInNeT  "}, "mainnet",
			"https://soroban-mainnet.stellar.org", rpc.PassphraseMainnet, ""},
		{"whitespace-only network defaults to testnet", map[string]string{"NETWORK": " \t "}, "testnet",
			"https://soroban-testnet.stellar.org", rpc.PassphraseTestnet, ""},
		{"empty overrides keep preset values",
			map[string]string{"NETWORK": "futurenet", "RPC_URL": "", "NETWORK_PASSPHRASE": ""}, "futurenet",
			"https://rpc-futurenet.stellar.org", rpc.PassphraseFuturenet, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseNetwork(func(k string) string { return tt.env[k] })
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("want error containing %q, got %v", tt.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Name != tt.wantName || got.RPCURL != tt.wantRPC || got.Passphrase != tt.wantPhrase {
				t.Fatalf("got %+v, want name=%s rpc=%s phrase=%s", got, tt.wantName, tt.wantRPC, tt.wantPhrase)
			}
		})
	}
}

func TestVerifyPassphrase(t *testing.T) {
	tests := []struct {
		name       string
		configured string
		reported   string
		wantErr    bool
		contains   []string
	}{
		{"matching testnet passphrases verify", rpc.PassphraseTestnet, rpc.PassphraseTestnet, false, nil},
		// Empty on either side means verification is skipped, not failed: the
		// node or the config may simply not report a passphrase.
		{"both empty skip verification", "", "", false, nil},
		{"empty configured skips verification", "", rpc.PassphraseTestnet, false, nil},
		{"empty reported skips verification", rpc.PassphraseTestnet, "", false, nil},
		{"identical unknown passphrases verify", "custom-network", "custom-network", false, nil},
		{"known network mismatch names both sides", rpc.PassphraseMainnet, rpc.PassphraseTestnet, true,
			[]string{"network mismatch", "mainnet", "testnet"}},
		{"unknown mismatch quotes both values", "custom-a", "custom-b", true,
			[]string{"network mismatch", `"custom-a"`, `"custom-b"`}},
		// Comparison is literal: a near-miss (here a trailing space) is still a
		// mismatch, so a typo in NETWORK_PASSPHRASE cannot pass silently.
		{"whitespace difference is a mismatch", rpc.PassphraseTestnet, rpc.PassphraseTestnet + " ", true,
			[]string{"network mismatch", "testnet"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := VerifyPassphrase(tt.configured, tt.reported)
			if !tt.wantErr {
				if err != nil {
					t.Fatalf("expected verification to pass, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected a network mismatch error, got nil")
			}
			for _, want := range tt.contains {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error %q should contain %q", err, want)
				}
			}
		})
	}
}
