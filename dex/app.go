// Copyright 2018 The dexon-consensus Authors
// This file is part of the dexon-consensus library.
//
// The dexon-consensus library is free software: you can redistribute it
// and/or modify it under the terms of the GNU Lesser General Public License as
// published by the Free Software Foundation, either version 3 of the License,
// or (at your option) any later version.
//
// The dexon-consensus library is distributed in the hope that it will be
// useful, but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the GNU Lesser
// General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the dexon-consensus library. If not, see
// <http://www.gnu.org/licenses/>.

package dex

import (
	"context"
	"fmt"
	"math/big"
	"sync"
	"time"

	coreCommon "github.com/dexon-foundation/dexon-consensus/common"
	coreTypes "github.com/dexon-foundation/dexon-consensus/core/types"

	"github.com/dexon-foundation/dexon/common"
	"github.com/dexon-foundation/dexon/core"
	"github.com/dexon-foundation/dexon/core/rawdb"
	"github.com/dexon-foundation/dexon/core/types"
	"github.com/dexon-foundation/dexon/ethdb"
	"github.com/dexon-foundation/dexon/event"
	"github.com/dexon-foundation/dexon/log"
	"github.com/dexon-foundation/dexon/rlp"
)

// DexconApp implements the DEXON consensus core application interface.
type DexconApp struct {
	txPool     *core.TxPool
	blockchain *core.BlockChain
	gov        *DexconGovernance
	chainDB    ethdb.Database
	config     *Config

	finalizedBlockFeed event.Feed
	scope              event.SubscriptionScope

	chainLocks sync.Map
	chainRoot  sync.Map
}

func NewDexconApp(txPool *core.TxPool, blockchain *core.BlockChain, gov *DexconGovernance,
	chainDB ethdb.Database, config *Config) *DexconApp {
	return &DexconApp{
		txPool:     txPool,
		blockchain: blockchain,
		gov:        gov,
		chainDB:    chainDB,
		config:     config,
	}
}

func (d *DexconApp) addrBelongsToChain(address common.Address, chainSize, chainID *big.Int) bool {
	return new(big.Int).Mod(address.Big(), chainSize).Cmp(chainID) == 0
}

func (d *DexconApp) chainLock(chainID uint32) {
	v, ok := d.chainLocks.Load(chainID)
	if !ok {
		v, _ = d.chainLocks.LoadOrStore(chainID, &sync.RWMutex{})
	}
	v.(*sync.RWMutex).Lock()
}

func (d *DexconApp) chainUnlock(chainID uint32) {
	v, ok := d.chainLocks.Load(chainID)
	if !ok {
		panic(fmt.Errorf("chain %v is not init yet", chainID))
	}
	v.(*sync.RWMutex).Unlock()
}

func (d *DexconApp) chainRLock(chainID uint32) {
	v, ok := d.chainLocks.Load(chainID)
	if !ok {
		v, _ = d.chainLocks.LoadOrStore(chainID, &sync.RWMutex{})
	}
	v.(*sync.RWMutex).RLock()
}

func (d *DexconApp) chainRUnlock(chainID uint32) {
	v, ok := d.chainLocks.Load(chainID)
	if !ok {
		panic(fmt.Errorf("chain %v is not init yet", chainID))
	}
	v.(*sync.RWMutex).RUnlock()
}

// validateNonce check if nonce is in order and return first nonce of every address.
func (d *DexconApp) validateNonce(txs types.Transactions) (map[common.Address]uint64, error) {
	addressFirstNonce := map[common.Address]uint64{}
	addressNonce := map[common.Address]uint64{}

	for _, tx := range txs {
		msg, err := tx.AsMessage(types.MakeSigner(d.blockchain.Config(), new(big.Int)))
		if err != nil {
			return nil, err
		}

		if _, exist := addressFirstNonce[msg.From()]; exist {
			if addressNonce[msg.From()]+1 != msg.Nonce() {
				return nil, fmt.Errorf("address nonce check error: expect %v actual %v",
					addressNonce[msg.From()]+1, msg.Nonce())
			}
			addressNonce[msg.From()] = msg.Nonce()
		} else {
			addressNonce[msg.From()] = msg.Nonce()
			addressFirstNonce[msg.From()] = msg.Nonce()
		}
	}
	return addressFirstNonce, nil
}

// PreparePayload is called when consensus core is preparing payload for block.
func (d *DexconApp) PreparePayload(position coreTypes.Position) (payload []byte, err error) {
	// softLimit limits the runtime of inner call to preparePayload.
	// hardLimit limits the runtime of outer PreparePayload.
	// If hardLimit is hit, it is possible that no payload is prepared.
	softLimit := 100 * time.Millisecond
	hardLimit := 150 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), hardLimit)
	defer cancel()
	payloadCh := make(chan []byte, 1)
	errCh := make(chan error, 1)
	doneCh := make(chan struct{}, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), softLimit)
		defer cancel()
		payload, err := d.preparePayload(ctx, position)
		if err != nil {
			errCh <- err
		}
		payloadCh <- payload
		doneCh <- struct{}{}
	}()
	select {
	case <-ctx.Done():
	case <-doneCh:
	}
	select {
	case err = <-errCh:
		if err != nil {
			return
		}
	default:
	}
	select {
	case payload = <-payloadCh:
	default:
	}
	return
}

func (d *DexconApp) preparePayload(ctx context.Context, position coreTypes.Position) (
	payload []byte, err error) {
	d.chainRLock(position.ChainID)
	defer d.chainRUnlock(position.ChainID)
	select {
	// This case will hit if previous RLock took too much time.
	case <-ctx.Done():
		return
	default:
	}

	if position.Round > 0 {
		// If round chain number changed but new round is not delivered yet, payload must be nil.
		previousNumChains := d.gov.Configuration(position.Round - 1).NumChains
		currentNumChains := d.gov.Configuration(position.Round).NumChains
		if previousNumChains != currentNumChains {
			deliveredRound, err := rawdb.ReadLastRoundNumber(d.chainDB)
			if err != nil {
				panic(fmt.Errorf("read current round error: %v", err))
			}

			if deliveredRound < position.Round {
				return nil, nil
			}
		}
	}

	if position.Height != 0 {
		// Check if chain block height is strictly increamental.
		chainLastHeight, ok := d.blockchain.GetChainLastConfirmedHeight(position.ChainID)
		if !ok || chainLastHeight != position.Height-1 {
			log.Debug("Previous confirmed block not exists", "current pos", position.String(),
				"prev height", chainLastHeight, "ok", ok)
			return nil, fmt.Errorf("previous block not exists")
		}
	}

	root, exist := d.chainRoot.Load(position.ChainID)
	if !exist {
		return nil, nil
	}

	currentState, err := d.blockchain.StateAt(*root.(*common.Hash))
	if err != nil {
		return nil, err
	}
	log.Debug("Prepare payload", "chain", position.ChainID, "height", position.Height)

	txsMap, err := d.txPool.Pending()
	if err != nil {
		return
	}

	chainID := new(big.Int).SetUint64(uint64(position.ChainID))
	chainNums := new(big.Int).SetUint64(uint64(d.gov.GetNumChains(position.Round)))
	blockGasLimit := new(big.Int).SetUint64(d.blockchain.CurrentBlock().GasLimit())
	blockGasUsed := new(big.Int)
	allTxs := make([]*types.Transaction, 0, 3000)

addressMap:
	for address, txs := range txsMap {
		select {
		case <-ctx.Done():
			break addressMap
		default:
		}
		// TX hash need to be slot to the given chain in order to be included in the block.
		if !d.addrBelongsToChain(address, chainNums, chainID) {
			continue
		}

		balance := currentState.GetBalance(address)
		cost, exist := d.blockchain.GetCostInConfirmedBlocks(position.ChainID, address)
		if exist {
			balance = new(big.Int).Sub(balance, cost)
		}

		var expectNonce uint64
		lastConfirmedNonce, exist := d.blockchain.GetLastNonceInConfirmedBlocks(position.ChainID, address)
		if !exist {
			expectNonce = currentState.GetNonce(address)
		} else {
			expectNonce = lastConfirmedNonce + 1
		}

		if len(txs) == 0 {
			continue
		}

		firstNonce := txs[0].Nonce()
		startIndex := int(expectNonce - firstNonce)

		// Warning: the pending tx will also affect by syncing, so startIndex maybe negative
		for i := startIndex; i >= 0 && i < len(txs); i++ {
			tx := txs[i]
			intrGas, err := core.IntrinsicGas(tx.Data(), tx.To() == nil, true)
			if err != nil {
				log.Error("Failed to calculate intrinsic gas", "error", err)
				return nil, fmt.Errorf("calculate intrinsic gas error: %v", err)
			}
			if tx.Gas() < intrGas {
				log.Error("Intrinsic gas too low", "txHash", tx.Hash().String())
				break
			}

			balance = new(big.Int).Sub(balance, tx.Cost())
			if balance.Cmp(big.NewInt(0)) < 0 {
				log.Warn("Insufficient funds for gas * price + value", "txHash", tx.Hash().String())
				break
			}

			blockGasUsed = new(big.Int).Add(blockGasUsed, big.NewInt(int64(tx.Gas())))
			if blockGasUsed.Cmp(blockGasLimit) > 0 {
				break addressMap
			}

			allTxs = append(allTxs, tx)
		}
	}

	return rlp.EncodeToBytes(&allTxs)
}

// PrepareWitness will return the witness data no lower than consensusHeight.
func (d *DexconApp) PrepareWitness(consensusHeight uint64) (witness coreTypes.Witness, err error) {
	var witnessBlock *types.Block
	if d.blockchain.CurrentBlock().NumberU64() >= consensusHeight {
		witnessBlock = d.blockchain.CurrentBlock()
	} else {
		log.Error("Current height too low", "lastPendingHeight", d.blockchain.CurrentBlock().NumberU64(),
			"consensusHeight", consensusHeight)
		return witness, fmt.Errorf("current height < consensus height")
	}

	witnessData, err := rlp.EncodeToBytes(witnessBlock.Hash())
	if err != nil {
		return
	}

	return coreTypes.Witness{
		Height: witnessBlock.NumberU64(),
		Data:   witnessData,
	}, nil
}

// VerifyBlock verifies if the payloads are valid.
func (d *DexconApp) VerifyBlock(block *coreTypes.Block) coreTypes.BlockVerifyStatus {
	var witnessBlockHash common.Hash
	err := rlp.DecodeBytes(block.Witness.Data, &witnessBlockHash)
	if err != nil {
		log.Error("Failed to RLP decode witness data", "error", err)
		return coreTypes.VerifyInvalidBlock
	}

	// Validate witness height.
	if d.blockchain.CurrentBlock().NumberU64() < block.Witness.Height {
		log.Debug("Current height < witness height")
		return coreTypes.VerifyRetryLater
	}

	b := d.blockchain.GetBlockByNumber(block.Witness.Height)
	if b == nil {
		log.Error("Can not get block by height", "height", block.Witness.Height)
		return coreTypes.VerifyInvalidBlock
	}

	if b.Hash() != witnessBlockHash {
		log.Error("Witness block hash not match",
			"expect", b.Hash().String(), "got", witnessBlockHash.String())
		return coreTypes.VerifyInvalidBlock
	}

	_, err = d.blockchain.StateAt(b.Root())
	if err != nil {
		log.Error("Get state by root %v error: %v", b.Root(), err)
		return coreTypes.VerifyInvalidBlock
	}

	d.chainRLock(block.Position.ChainID)
	defer d.chainRUnlock(block.Position.ChainID)

	if block.Position.Height != 0 {
		// Check if target block is the next height to be verified, we can only
		// verify the next block in a given chain.
		chainLastHeight, ok := d.blockchain.GetChainLastConfirmedHeight(block.Position.ChainID)
		if !ok || chainLastHeight != block.Position.Height-1 {
			log.Debug("Previous confirmed block not exists", "current pos", block.Position.String(),
				"prev height", chainLastHeight, "ok", ok)
			return coreTypes.VerifyRetryLater
		}
	}

	if block.Position.Round > 0 {
		// If round chain number changed but new round is not delivered yet, payload must be nil.
		previousNumChains := d.gov.Configuration(block.Position.Round - 1).NumChains
		currentNumChains := d.gov.Configuration(block.Position.Round).NumChains
		if previousNumChains != currentNumChains {
			deliveredRound, err := rawdb.ReadLastRoundNumber(d.chainDB)
			if err != nil {
				panic(fmt.Errorf("read current round error: %v", err))
			}

			if deliveredRound < block.Position.Round {
				if len(block.Payload) > 0 {
					return coreTypes.VerifyInvalidBlock
				}

				return coreTypes.VerifyOK
			}
		}
	}

	// Get latest state with current chain.
	root, exist := d.chainRoot.Load(block.Position.ChainID)
	if !exist {
		return coreTypes.VerifyRetryLater
	}

	currentState, err := d.blockchain.StateAt(*root.(*common.Hash))
	log.Debug("Verify block", "chain", block.Position.ChainID, "height", block.Position.Height)
	if err != nil {
		log.Debug("Invalid state root", "root", *root.(*common.Hash), "err", err)
		return coreTypes.VerifyInvalidBlock
	}

	var transactions types.Transactions
	if len(block.Payload) == 0 {
		return coreTypes.VerifyOK
	}
	err = rlp.DecodeBytes(block.Payload, &transactions)
	if err != nil {
		log.Error("Payload rlp decode", "error", err)
		return coreTypes.VerifyInvalidBlock
	}

	_, err = types.GlobalSigCache.Add(types.NewEIP155Signer(d.blockchain.Config().ChainID), transactions)
	if err != nil {
		log.Error("Failed to calculate sender", "error", err)
		return coreTypes.VerifyInvalidBlock
	}

	addressNonce, err := d.validateNonce(transactions)
	if err != nil {
		log.Error("Validate nonce failed", "error", err)
		return coreTypes.VerifyInvalidBlock
	}

	// Check if nonce is strictly increasing for every address.
	chainID := big.NewInt(int64(block.Position.ChainID))
	chainNums := big.NewInt(int64(d.gov.GetNumChains(block.Position.Round)))

	for address, firstNonce := range addressNonce {
		if !d.addrBelongsToChain(address, chainNums, chainID) {
			log.Error("Address does not belong to given chain ID", "address", address, "chainD", chainID)
			return coreTypes.VerifyInvalidBlock
		}

		var expectNonce uint64
		lastConfirmedNonce, exist := d.blockchain.GetLastNonceInConfirmedBlocks(block.Position.ChainID, address)
		if exist {
			expectNonce = lastConfirmedNonce + 1
		} else {
			expectNonce = currentState.GetNonce(address)
		}

		if expectNonce != firstNonce {
			log.Error("Nonce check error", "expect", expectNonce, "firstNonce", firstNonce)
			return coreTypes.VerifyInvalidBlock
		}
	}

	// Calculate balance in last state (including pending state).
	addressesBalance := map[common.Address]*big.Int{}
	for address := range addressNonce {
		cost, exist := d.blockchain.GetCostInConfirmedBlocks(block.Position.ChainID, address)
		if exist {
			addressesBalance[address] = new(big.Int).Sub(currentState.GetBalance(address), cost)
		} else {
			addressesBalance[address] = currentState.GetBalance(address)
		}
	}

	// Validate if balance is enough for TXs in this block.
	blockGasLimit := new(big.Int).SetUint64(d.blockchain.CurrentBlock().GasLimit())
	blockGasUsed := new(big.Int)

	for _, tx := range transactions {
		msg, err := tx.AsMessage(types.MakeSigner(d.blockchain.Config(), new(big.Int)))
		if err != nil {
			log.Error("Failed to convert tx to message", "error", err)
			return coreTypes.VerifyInvalidBlock
		}
		balance := addressesBalance[msg.From()]
		intrGas, err := core.IntrinsicGas(msg.Data(), msg.To() == nil, true)
		if err != nil {
			log.Error("Failed to calculate intrinsic gas", "err", err)
			return coreTypes.VerifyInvalidBlock
		}
		if tx.Gas() < intrGas {
			log.Error("Intrinsic gas too low", "txHash", tx.Hash().String(), "intrinsic", intrGas, "gas", tx.Gas())
			return coreTypes.VerifyInvalidBlock
		}

		balance = new(big.Int).Sub(balance, tx.Cost())
		if balance.Cmp(big.NewInt(0)) < 0 {
			log.Error("Insufficient funds for gas * price + value", "txHash", tx.Hash().String())
			return coreTypes.VerifyInvalidBlock
		}

		blockGasUsed = new(big.Int).Add(blockGasUsed, new(big.Int).SetUint64(tx.Gas()))
		if blockGasUsed.Cmp(blockGasLimit) > 0 {
			log.Error("Reach block gas limit", "gasUsed", blockGasUsed)
			return coreTypes.VerifyInvalidBlock
		}
		addressesBalance[msg.From()] = balance
	}

	return coreTypes.VerifyOK
}

// BlockDelivered is called when a block is add to the compaction chain.
func (d *DexconApp) BlockDelivered(
	blockHash coreCommon.Hash,
	blockPosition coreTypes.Position,
	result coreTypes.FinalizationResult) {

	log.Debug("DexconApp block deliver", "height", result.Height, "hash", blockHash, "position", blockPosition.String())
	defer log.Debug("DexconApp block delivered", "height", result.Height, "hash", blockHash, "position", blockPosition.String())

	chainID := blockPosition.ChainID
	d.chainLock(chainID)
	defer d.chainUnlock(chainID)

	block, txs := d.blockchain.GetConfirmedBlockByHash(chainID, blockHash)
	if block == nil {
		panic("Can not get confirmed block")
	}

	block.Payload = nil
	block.Finalization = result
	dexconMeta, err := rlp.EncodeToBytes(block)
	if err != nil {
		panic(err)
	}

	newBlock := types.NewBlock(&types.Header{
		Number:     new(big.Int).SetUint64(result.Height),
		Time:       big.NewInt(result.Timestamp.UnixNano() / 1000000),
		Coinbase:   common.BytesToAddress(block.ProposerID.Bytes()),
		GasLimit:   d.gov.DexconConfiguration(block.Position.Round).BlockGasLimit,
		Difficulty: big.NewInt(1),
		Round:      block.Position.Round,
		DexconMeta: dexconMeta,
		Randomness: result.Randomness,
	}, txs, nil, nil)

	h := d.blockchain.CurrentBlock().NumberU64() + 1
	var root *common.Hash
	if block.IsEmpty() {
		root, err = d.blockchain.ProcessEmptyBlock(newBlock)
		if err != nil {
			log.Error("Failed to process empty block", "error", err)
			panic(err)
		}
	} else {
		root, err = d.blockchain.ProcessBlock(newBlock, &block.Witness)
		if err != nil {
			log.Error("Failed to process pending block", "error", err)
			panic(err)
		}
	}
	d.chainRoot.Store(chainID, root)

	d.blockchain.RemoveConfirmedBlock(chainID, blockHash)

	// New blocks are finalized, notify other components.
	newHeight := d.blockchain.CurrentBlock().NumberU64()
	for h <= newHeight {
		b := d.blockchain.GetBlockByNumber(h)
		go d.finalizedBlockFeed.Send(core.NewFinalizedBlockEvent{b})
		log.Debug("Send new finalized block event", "number", h)
		h++
	}
}

// BlockConfirmed is called when a block is confirmed and added to lattice.
func (d *DexconApp) BlockConfirmed(block coreTypes.Block) {
	d.chainLock(block.Position.ChainID)
	defer d.chainUnlock(block.Position.ChainID)

	log.Debug("DexconApp block confirmed", "block", block.String())
	if err := d.blockchain.AddConfirmedBlock(&block); err != nil {
		panic(err)
	}
}

func (d *DexconApp) SubscribeNewFinalizedBlockEvent(
	ch chan<- core.NewFinalizedBlockEvent) event.Subscription {
	return d.scope.Track(d.finalizedBlockFeed.Subscribe(ch))
}

func (d *DexconApp) Stop() {
	d.scope.Close()
}
