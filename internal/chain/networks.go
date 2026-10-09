package chain

import (
	"fmt"

	"github.com/OmarIbannez/puente/internal/money"
)

// Network is one (chain, token) pair Puente watches for deposits of one
// asset.
type Network struct {
	// Key identifies the network in config, URLs and the chain_transfers
	// table, e.g. "base-sepolia-usdc".
	Key string `json:"key"`
	// Name is the human-readable name.
	Name string `json:"name"`
	// ChainID is the EIP-155 chain id.
	ChainID int64 `json:"chain_id"`
	// Asset is the stablecoin the token represents.
	Asset money.Asset `json:"asset"`
	// Token is the ERC-20 contract address (lowercase hex).
	Token string `json:"token"`
	// ExplorerTxURL is a fmt pattern taking a transaction hash, or empty
	// when the network has no public explorer.
	ExplorerTxURL string `json:"explorer_tx_url,omitempty"`
	// Simulated is true for the per-sandbox simulated chains.
	Simulated bool `json:"simulated"`
}

// Network keys.
const (
	// KeyBaseSepoliaUSDC is Circle's USDC on Base Sepolia.
	KeyBaseSepoliaUSDC = "base-sepolia-usdc"
	// KeyArbitrumSepoliaMXNB is Bitso's MXNB on Arbitrum Sepolia.
	KeyArbitrumSepoliaMXNB = "arbitrum-sepolia-mxnb"
	// KeySimUSDC is USDC on the simulated chain.
	KeySimUSDC = "sim-usdc"
	// KeySimMXNB is MXNB on the simulated chain.
	KeySimMXNB = "sim-mxnb"
)

// Simulated chain ids. They are outside the range of any public chain
// Puente talks to, so a simulated transfer can never collide with a real one.
const (
	// SimUSDCChainID is the chain id of the simulated USDC chain.
	SimUSDCChainID int64 = 990_001
	// SimMXNBChainID is the chain id of the simulated MXNB chain.
	SimMXNBChainID int64 = 990_002
)

// ERC-20 contract addresses of the live networks (verified on-chain, see
// docs/discovery/2026-10-08-puente-v1.md, Assumptions).
const (
	usdcBaseSepoliaContract     = "0x036cbd53842c5426634e7929541ec2318f3dcf7e"
	mxnbArbitrumSepoliaContract = "0x82b9e52b26a2954e113f94ff26647754d5a4247d"
)

// Networks are every network Puente knows, in display order.
var Networks = []Network{
	{
		Key:           KeyBaseSepoliaUSDC,
		Name:          "USDC on Base Sepolia",
		ChainID:       84532,
		Asset:         money.USDC,
		Token:         usdcBaseSepoliaContract,
		ExplorerTxURL: "https://sepolia.basescan.org/tx/%s",
	},
	{
		Key:           KeyArbitrumSepoliaMXNB,
		Name:          "MXNB on Arbitrum Sepolia",
		ChainID:       421614,
		Asset:         money.MXNB,
		Token:         mxnbArbitrumSepoliaContract,
		ExplorerTxURL: "https://sepolia.arbiscan.io/tx/%s",
	},
	{
		Key:       KeySimUSDC,
		Name:      "USDC on the simulated chain",
		ChainID:   SimUSDCChainID,
		Asset:     money.USDC,
		Token:     "0x5157000000000000000000000000000000005dc1",
		Simulated: true,
	},
	{
		Key:       KeySimMXNB,
		Name:      "MXNB on the simulated chain",
		ChainID:   SimMXNBChainID,
		Asset:     money.MXNB,
		Token:     "0x5157000000000000000000000000000000003e8b",
		Simulated: true,
	},
}

// NetworkByKey returns the network with key, or false.
func NetworkByKey(key string) (Network, bool) {
	for _, n := range Networks {
		if n.Key == key {
			return n, true
		}
	}
	return Network{}, false
}

// SimNetwork returns the simulated network of asset, or false when the
// asset is not a stablecoin.
func SimNetwork(asset money.Asset) (Network, bool) {
	for _, n := range Networks {
		if n.Simulated && n.Asset == asset {
			return n, true
		}
	}
	return Network{}, false
}

// TxURL returns the explorer link for txHash, or "" when the network has no
// explorer.
func (n Network) TxURL(txHash string) string {
	if n.ExplorerTxURL == "" {
		return ""
	}
	return fmt.Sprintf(n.ExplorerTxURL, txHash)
}
