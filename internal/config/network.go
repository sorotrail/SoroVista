package config

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/sorotrail/sorovista/internal/rpc"
)

// Network describes one Stellar network: the passphrase that identifies it
// and the public RPC endpoint that serves it.
type Network struct {
	// Name is the short label used in config and logs (testnet, mainnet,
	// futurenet, custom).
	Name string
	// Passphrase is the network passphrase — a Stellar network's identity.
	Passphrase string
	// RPCURL is the public RPC endpoint for this network.
	RPCURL string
}

// KnownNetworks are the SDF-maintained networks with public RPC endpoints.
var KnownNetworks = []Network{
	{Name: "testnet", Passphrase: rpc.PassphraseTestnet, RPCURL: "https://soroban-testnet.stellar.org"},
	{Name: "mainnet", Passphrase: rpc.PassphraseMainnet, RPCURL: "https://soroban-mainnet.stellar.org"},
	{Name: "futurenet", Passphrase: rpc.PassphraseFuturenet, RPCURL: "https://rpc-futurenet.stellar.org"},
}

// NetworkByName returns the known network with the given name, or nil.
func NetworkByName(name string) *Network {
	for i, n := range KnownNetworks {
		if n.Name == name {
			return &KnownNetworks[i]
		}
	}
	return nil
}

// ParseNetwork resolves the NETWORK, RPC_URL and NETWORK_PASSPHRASE
// variables into a Network. The rules:
//
//   - NETWORK unset or "testnet": the testnet defaults, as before.
//   - NETWORK one of mainnet/futurenet: that network's public RPC and
//     passphrase, unless RPC_URL overrides the endpoint.
//   - NETWORK "custom": RPC_URL and NETWORK_PASSPHRASE must both be set —
//     for private standalone networks with a passphrase of your choosing.
//   - NETWORK_PASSPHRASE always wins over the preset, so a named network
//     with a custom passphrase (a local quickstart testnet) composes.
//
// The network only matters in standalone mode; upstream mode reads from a
// SoroTrail indexer whose own deployment owns its network.
func ParseNetwork(getenv func(string) string) (Network, error) {
	name := strings.ToLower(strings.TrimSpace(getenv("NETWORK")))
	if name == "" {
		name = "testnet"
	}

	var net Network
	switch name {
	case "testnet", "mainnet", "futurenet":
		net = *NetworkByName(name)
	case "custom":
		net = Network{Name: "custom"}
	default:
		return Network{}, fmt.Errorf("invalid NETWORK %q (want testnet|mainnet|futurenet|custom)", name)
	}

	if v := getenv("RPC_URL"); v != "" {
		net.RPCURL = v
	}
	if v := getenv("NETWORK_PASSPHRASE"); v != "" {
		net.Passphrase = v
	}

	if net.RPCURL == "" {
		return Network{}, fmt.Errorf("RPC_URL is required when NETWORK=%s", name)
	}
	if net.Passphrase == "" {
		return Network{}, fmt.Errorf("NETWORK_PASSPHRASE is required when NETWORK=custom")
	}
	return net, nil
}

// VerifyPassphrase reports whether the passphrase reported by the connected
// RPC node matches the configured one. A mismatch means the deployment is
// pointed at a different network than it believes — in standalone mode that
// makes the explorer quietly show the wrong chain's activity.
func VerifyPassphrase(configured, reported string) error {
	if configured == "" || reported == "" {
		return nil
	}
	if configured != reported {
		label := func(p string) string {
			if n := rpc.NetworkName(p); n != "" {
				return fmt.Sprintf("%s (%q)", n, p)
			}
			return fmt.Sprintf("%q", p)
		}
		return fmt.Errorf(
			"network mismatch: configured for %s but the RPC endpoint belongs to %s — check NETWORK / RPC_URL",
			label(configured), label(reported))
	}
	return nil
}

// ErrNetworkUnverifiable marks a startup network check where the endpoint
// could not be asked for its passphrase. Startup proceeds — the poller will
// surface the outage — but callers log the error as a warning: an explorer
// quietly missing chain activity is the failure this check exists to catch.
var ErrNetworkUnverifiable = errors.New("network passphrase unverifiable")

// StartupNetworkCheck performs the startup network check: it asks the
// connected RPC node for its passphrase and compares it with the configured
// one. A mismatch is fatal — proceeding would make the explorer quietly show
// the wrong chain's activity as if the watched contracts simply had none.
// An endpoint that cannot be queried returns an error wrapping
// ErrNetworkUnverifiable: the decision is deliberately non-fatal, and the
// caller logs it as a warning.
func StartupNetworkCheck(ctx context.Context, client *rpc.HTTPClient, configuredPassphrase string) error {
	net, err := client.GetNetwork(ctx)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrNetworkUnverifiable, err)
	}
	return VerifyPassphrase(configuredPassphrase, net.Passphrase)
}
