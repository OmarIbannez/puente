package evm

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"fmt"
	"math/big"

	"github.com/OmarIbannez/puente/internal/chain"
	"github.com/OmarIbannez/puente/internal/money"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

// ErrBadSend is returned by Send for an invalid amount or recipient.
var ErrBadSend = errors.New("evm: invalid send")

// transferSelector is the 4-byte selector of transfer(address,uint256).
var transferSelector = crypto.Keccak256([]byte("transfer(address,uint256)"))[:4]

// SendClient is the write side of an RPC client. *ethclient.Client and
// go-ethereum's simulated.Client both satisfy it.
type SendClient interface {
	ChainID(ctx context.Context) (*big.Int, error)
	HeaderByNumber(ctx context.Context, number *big.Int) (*types.Header, error)
	PendingNonceAt(ctx context.Context, account common.Address) (uint64, error)
	SuggestGasTipCap(ctx context.Context) (*big.Int, error)
	EstimateGas(ctx context.Context, msg ethereum.CallMsg) (uint64, error)
	SendTransaction(ctx context.Context, tx *types.Transaction) error
}

// TransferCalldata returns the calldata of an ERC-20 transfer(to, amount).
func TransferCalldata(to common.Address, amount *big.Int) []byte {
	data := make([]byte, 0, 4+32+32)
	data = append(data, transferSelector...)
	data = append(data, common.LeftPadBytes(to.Bytes(), 32)...)
	return append(data, common.LeftPadBytes(amount.Bytes(), 32)...)
}

// Send signs and broadcasts an ERC-20 transfer of amount of net's token from
// key's address to to, as an EIP-1559 transaction (max fee = 2 × base fee +
// tip, gas = estimate + 20%). It returns the transaction hash without
// waiting for it to be mined. Only the CLI sends; the API never does.
func Send(ctx context.Context, client SendClient, key *ecdsa.PrivateKey, net chain.Network, to string, amount money.Amount) (string, error) {
	if amount.Asset != net.Asset || amount.Minor <= 0 {
		return "", fmt.Errorf("%w: amount %s on %s", ErrBadSend, amount, net.Key)
	}
	toAddr, err := chain.NormalizeAddress(to)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrBadSend, err)
	}
	if err := checkChainID(ctx, client, net); err != nil {
		return "", err
	}
	from := crypto.PubkeyToAddress(key.PublicKey)
	token := common.HexToAddress(net.Token)
	data := TransferCalldata(common.HexToAddress(toAddr), big.NewInt(amount.Minor))

	head, err := client.HeaderByNumber(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("evm: send: head: %w", err)
	}
	if head.BaseFee == nil {
		return "", fmt.Errorf("evm: send: %s has no EIP-1559 base fee", net.Key)
	}
	tip, err := client.SuggestGasTipCap(ctx)
	if err != nil {
		return "", fmt.Errorf("evm: send: gas tip: %w", err)
	}
	feeCap := new(big.Int).Add(new(big.Int).Mul(head.BaseFee, big.NewInt(2)), tip)
	nonce, err := client.PendingNonceAt(ctx, from)
	if err != nil {
		return "", fmt.Errorf("evm: send: nonce: %w", err)
	}
	gas, err := client.EstimateGas(ctx, ethereum.CallMsg{
		From: from, To: &token, GasFeeCap: feeCap, GasTipCap: tip, Data: data,
	})
	if err != nil {
		return "", fmt.Errorf("evm: send: estimate gas: %w", err)
	}
	chainID := big.NewInt(net.ChainID)
	tx, err := types.SignNewTx(key, types.LatestSignerForChainID(chainID), &types.DynamicFeeTx{
		ChainID:   chainID,
		Nonce:     nonce,
		GasTipCap: tip,
		GasFeeCap: feeCap,
		Gas:       gas + gas/5,
		To:        &token,
		Value:     new(big.Int),
		Data:      data,
	})
	if err != nil {
		return "", fmt.Errorf("evm: send: sign: %w", err)
	}
	if err := client.SendTransaction(ctx, tx); err != nil {
		return "", fmt.Errorf("evm: send: %w", err)
	}
	return tx.Hash().Hex(), nil
}
