package blockchain

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	ec "github.com/ethereum/go-ethereum/ethclient"
	"github.com/renproject/darknode"
	"github.com/renproject/darknode/abi"
	"github.com/renproject/darknode/ethrpc"
	"github.com/renproject/darknode/ethrpc/bindings"
	"github.com/renproject/mercury/sdk/client/btcclient"
	"github.com/renproject/mercury/sdk/client/ethclient"
	"github.com/renproject/mercury/sdk/gateway/btcgateway"
	"github.com/renproject/mercury/types"
	"github.com/renproject/mercury/types/btctypes"
	"github.com/renproject/mercury/types/ethtypes"
	"github.com/sirupsen/logrus"
)

// ConnPool consolidates all blockchain clients and abstract all blockchain
// related interaction.
type ConnPool struct {
	logger     logrus.FieldLogger
	ethClient  ethclient.Client
	btcClient  btcclient.Client
	zecClient  btcclient.Client
	bchClient  btcclient.Client
	btcShifter *bindings.Shifter
	zecShifter *bindings.Shifter
	bchShifter *bindings.Shifter
}

// New creates a new ConnPool object of given network. It
func New(logger logrus.FieldLogger, network darknode.Network, protocolContract common.Address) ConnPool {
	btcClient := btcclient.NewClient(logger, btcNetwork(types.Bitcoin, network))
	zecClient := btcclient.NewClient(logger, btcNetwork(types.ZCash, network))
	bchClient := btcclient.NewClient(logger, btcNetwork(types.BitcoinCash, network))

	// Initialize Ethereum client and contracts.
	ethClient, err := ethclient.New(logger, ethNetwork(network))
	if err != nil {
		logger.Panicf("[connPool] cannot connect to Ethereum, err = %v", err)
	}
	protocol, err := ethrpc.NewProtocol(ethClient.EthClient(), protocolContract)
	if err != nil {
		logger.Panicf("[connPool] cannot initialize protocol contract bindings, err = %v", err)
	}
	shiftRegistryAddr, err := protocol.ShifterRegistry()
	if err != nil {
		logger.Panicf("[connPool] cannot read shifter registry contract address from protocol contract, err = %v", err)
	}
	shifterRegistry, err := ethrpc.NewShifterRegistry(ethClient.EthClient(), shiftRegistryAddr)
	if err != nil {
		panic(fmt.Errorf("cannot initialize shifterRegistry bindings: %v", err))
	}

	return ConnPool{
		logger:     logger,
		ethClient:  ethClient,
		btcClient:  btcClient,
		zecClient:  zecClient,
		bchClient:  bchClient,
		btcShifter: initShifter(shifterRegistry, "zBTC", ethClient.EthClient()),
		zecShifter: initShifter(shifterRegistry, "zZEC", ethClient.EthClient()),
		bchShifter: initShifter(shifterRegistry, "zBCH", ethClient.EthClient()),
	}
}

// ShiftOut filters the logs from the Shifter contract (according to the `addr`)
// and try to find ShiftOut log with given `ref`.
func (cp ConnPool) ShiftOut(addr abi.Address, ref uint64) ([]byte, uint64, error) {
	shifter := cp.ShifterByAddress(addr)
	shiftID := big.NewInt(int64(ref))

	// Filter all ShiftOut logs with given ref.
	iter, err := shifter.FilterLogShiftOut(nil, []*big.Int{shiftID}, nil)
	if err != nil {
		return nil, 0, err
	}

	// Loop through the logs and return the first one.(should only have one)
	for iter.Next() {
		to := iter.Event.To
		amount := iter.Event.Amount
		return to, amount.Uint64(), nil
	}

	return nil, 0, fmt.Errorf("invalid ref, no event with ref =%v can be found", ref)
}

// Utxo validates if the given txHash and vout are valid and returns the full
// details of the utxo. Note it will return an error if the utxo has been spent.
func (cp ConnPool) Utxo(ctx context.Context, addr abi.Address, hash abi.B32, vout abi.U32) (btctypes.UTXO, error) {
	client := cp.ClientByAddress(addr)
	txHash := types.TxHash(hex.EncodeToString(hash[:]))
	outpoint := btctypes.NewOutPoint(txHash, uint32(vout.Int.Uint64()))
	return client.UTXO(ctx, outpoint)
}

// UtxoConfirmations returns the number of confirmations of the given txHash.
func (cp ConnPool) UtxoConfirmations(ctx context.Context, addr abi.Address, hash abi.B32) (uint64, error) {
	client := cp.ClientByAddress(addr)
	txHash := types.TxHash(hex.EncodeToString(hash[:]))
	return client.Confirmations(ctx, txHash)
}

// EventConfirmations return the number of confirmations of the event log on
// Ethereum.
func (cp ConnPool) EventConfirmations(ctx context.Context, addr abi.Address, ref uint64) (uint64, error) {
	shifter := cp.ShifterByAddress(addr)
	shiftID := big.NewInt(int64(ref))

	// Get latest block number
	latestBlock, err := cp.ethClient.BlockNumber(ctx)
	if err != nil {
		return 0, err
	}

	// Find the event log which has given ref.
	opts := &bind.FilterOpts{
		Context: ctx,
	}
	iter, err := shifter.FilterLogShiftOut(opts, []*big.Int{shiftID}, nil)
	if err != nil {
		return 0, err
	}

	// Loop through the logs and return block difference(should only have one)
	for iter.Next() {
		eventBlock := iter.Event.Raw.BlockNumber
		return latestBlock.Uint64() - eventBlock, nil
	}

	return 0, fmt.Errorf("invalid ref, no event with ref =%v can be found", ref)
}

// VerifyScriptPubKey verifies if the utxo can be spent by the given distPubKey
// along with the ghash.
func (cp ConnPool) VerifyScriptPubKey(addr abi.Address, ghash []byte, distPubKey ecdsa.PublicKey, utxo btctypes.UTXO) error {
	client := cp.ClientByAddress(addr)
	gateway := btcgateway.New(client, distPubKey, ghash)
	expectedSPK, err := btctypes.PayToAddrScript(gateway.Address(), client.Network())
	if err != nil {
		return err
	}
	if !bytes.Equal(expectedSPK, utxo.ScriptPubKey()) {
		return errors.New("invalid scriptPubkey")
	}
	return nil
}

// ClientByAddress returns the proper blockchain client for the given Ren-VM
// contract address.
func (cp ConnPool) ClientByAddress(addr abi.Address) btcclient.Client {
	switch addr {
	case abi.IntrinsicBTC0Btc2Eth.Address:
		return cp.btcClient
	case abi.IntrinsicZEC0Zec2Eth.Address:
		return cp.zecClient
	case abi.IntrinsicBCH0Bch2Eth.Address:
		return cp.bchClient
	default:
		return nil
	}
}

// ShifterByAddress returns the proper shifter contract bindings for the given
// Ren-VM contract address.
func (cp ConnPool) ShifterByAddress(addr abi.Address) *bindings.Shifter {
	switch addr {
	case abi.IntrinsicBTC0Eth2Btc.Address:
		return cp.btcShifter
	case abi.IntrinsicZEC0Eth2Zec.Address:
		return cp.zecShifter
	case abi.IntrinsicBCH0Eth2Bch.Address:
		return cp.bchShifter
	default:
		cp.logger.Panicf("[validator] invalid shiftOut address = %v", addr)
		return nil
	}
}

func (cp ConnPool) EthClient() *ec.Client {
	return cp.ethClient.EthClient()
}

// btcNetwork returns the specific btc-like blockchain network depending on the
// blockchain and given darknode network.
func btcNetwork(chain types.Chain, network darknode.Network) btctypes.Network {
	switch network {
	case darknode.Localnet:
		return btctypes.NewNetwork(chain, "localnet")
	case darknode.Devnet, darknode.Testnet:
		return btctypes.NewNetwork(chain, "testnet")
	case darknode.Chaosnet, darknode.Mainnet:
		return btctypes.NewNetwork(chain, "mainnet")
	default:
		panic(fmt.Sprintf("unknown network =%v", network))
	}
}

// IsShiftIn returns if the given RenVM tx is a ShiftIn tx.
func IsShiftIn(tx abi.Tx) bool {
	switch tx.To {
	case abi.IntrinsicBTC0Btc2Eth.Address, abi.IntrinsicZEC0Zec2Eth.Address, abi.IntrinsicBCH0Bch2Eth.Address:
		return true
	case abi.IntrinsicBTC0Eth2Btc.Address, abi.IntrinsicZEC0Eth2Zec.Address, abi.IntrinsicBCH0Eth2Bch.Address:
		return false
	default:
		panic(fmt.Sprintf("[connPool] expected contract address = %v", tx.To))
	}
}

// initShifter reads shifter address of the token with given symbol from the
// shifterRegistry and initialize a bindings to interact with the specific
// shifter contract.
func initShifter(shifterRegistry *ethrpc.ShifterRegistry, symbol string, client *ec.Client) *bindings.Shifter {
	addr, err := shifterRegistry.ShifterAddressBySymbol(symbol)
	if err != nil {
		panic(fmt.Sprintf("[connPool] cannot get address of %v shifter contract: %v", symbol, err))
	}
	shifter, err := bindings.NewShifter(addr, client)
	if err != nil {
		panic(fmt.Sprintf("[connPool] cannot initialize %v shifter, err = %v", symbol, err))
	}
	return shifter
}

// ethNetwork returns the ethereum network of the given darknode network.
func ethNetwork(network darknode.Network) ethtypes.Network {
	switch network {
	case darknode.Localnet:
		return ethtypes.Ganache
	case darknode.Devnet, darknode.Testnet:
		return ethtypes.Kovan
	case darknode.Chaosnet, darknode.Mainnet:
		return ethtypes.Mainnet
	default:
		panic(fmt.Sprintf("unknown network =%v", network))
	}
}
