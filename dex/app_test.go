package dex

import (
	"crypto/ecdsa"
	"fmt"
	"math/big"
	"testing"
	"time"

	coreCommon "github.com/dexon-foundation/dexon-consensus/common"
	coreTypes "github.com/dexon-foundation/dexon-consensus/core/types"

	"github.com/dexon-foundation/dexon/common"
	"github.com/dexon-foundation/dexon/consensus/dexcon"
	"github.com/dexon-foundation/dexon/core"
	"github.com/dexon-foundation/dexon/core/types"
	"github.com/dexon-foundation/dexon/core/vm"
	"github.com/dexon-foundation/dexon/crypto"
	"github.com/dexon-foundation/dexon/ethdb"
	"github.com/dexon-foundation/dexon/params"
	"github.com/dexon-foundation/dexon/rlp"
)

func TestPreparePayload(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("hex to ecdsa error: %v", err)
	}

	dex, err := newTestDexonWithGenesis(key)
	if err != nil {
		t.Fatalf("new test dexon error: %v", err)
	}

	signer := types.NewEIP155Signer(dex.chainConfig.ChainID)

	var expectTx types.Transactions
	for i := 0; i < 5; i++ {
		tx, err := addTx(dex, i, signer, key)
		if err != nil {
			t.Fatalf("add tx error: %v", err)
		}
		expectTx = append(expectTx, tx)
	}

	// This transaction will not be included.
	_, err = addTx(dex, 100, signer, key)
	if err != nil {
		t.Fatalf("add tx error: %v", err)
	}

	chainNum := uint32(0)
	root := dex.blockchain.CurrentBlock().Root()
	dex.app.chainRoot.Store(chainNum, &root)
	payload, err := dex.app.PreparePayload(coreTypes.Position{})
	if err != nil {
		t.Fatalf("prepare payload error: %v", err)
	}

	var transactions types.Transactions
	err = rlp.DecodeBytes(payload, &transactions)
	if err != nil {
		t.Fatalf("rlp decode error: %v", err)
	}

	// Only one chain id allow prepare transactions.
	if len(transactions) != 5 {
		t.Fatalf("incorrect transaction num expect %v but %v", 5, len(transactions))
	}

	for i, tx := range transactions {
		if expectTx[i].Gas() != tx.Gas() {
			t.Fatalf("unexpected gas expect %v but %v", expectTx[i].Gas(), tx.Gas())
		}

		if expectTx[i].Hash() != tx.Hash() {
			t.Fatalf("unexpected hash expect %v but %v", expectTx[i].Hash(), tx.Hash())
		}

		if expectTx[i].Nonce() != tx.Nonce() {
			t.Fatalf("unexpected nonce expect %v but %v", expectTx[i].Nonce(), tx.Nonce())
		}

		if expectTx[i].GasPrice().Uint64() != tx.GasPrice().Uint64() {
			t.Fatalf("unexpected gas price expect %v but %v",
				expectTx[i].GasPrice().Uint64(), tx.GasPrice().Uint64())
		}
	}
}

func TestPrepareWitness(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("hex to ecdsa error: %v", err)
	}

	dex, err := newTestDexonWithGenesis(key)
	if err != nil {
		t.Fatalf("new test dexon error: %v", err)
	}

	currentBlock := dex.blockchain.CurrentBlock()

	witness, err := dex.app.PrepareWitness(0)
	if err != nil {
		t.Fatalf("prepare witness error: %v", err)
	}

	if witness.Height != currentBlock.NumberU64() {
		t.Fatalf("unexpeted witness height %v", witness.Height)
	}

	var witnessBlockHash common.Hash
	err = rlp.DecodeBytes(witness.Data, &witnessBlockHash)
	if err != nil {
		t.Fatalf("rlp decode error: %v", err)
	}

	if witnessBlockHash != currentBlock.Hash() {
		t.Fatalf("expect root %v but %v", currentBlock.Hash(), witnessBlockHash)
	}

	if _, err := dex.app.PrepareWitness(999); err == nil {
		t.Fatalf("it must be get error from prepare")
	} else {
		t.Logf("Nice error: %v", err)
	}
}

func TestVerifyBlock(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("hex to ecdsa error: %v", err)
	}

	dex, err := newTestDexonWithGenesis(key)
	if err != nil {
		t.Fatalf("new test dexon error: %v", err)
	}

	chainID := big.NewInt(0)

	root := dex.blockchain.CurrentBlock().Root()
	dex.app.chainRoot.Store(uint32(chainID.Uint64()), &root)

	// Prepare first confirmed block.
	_, err = prepareConfirmedBlocks(dex, []*ecdsa.PrivateKey{key}, 0)
	if err != nil {
		t.Fatalf("prepare confirmed blocks error: %v", err)
	}

	// Prepare normal block.
	block := &coreTypes.Block{}
	block.Hash = coreCommon.NewRandomHash()
	block.Position.Height = 1
	block.ProposerID = coreTypes.NodeID{coreCommon.Hash{1, 2, 3}}
	block.Payload, block.Witness, err = prepareDataWithoutTxPool(dex, key, 0, 100)
	if err != nil {
		t.Fatalf("prepare data error: %v", err)
	}

	// Expect ok.
	status := dex.app.VerifyBlock(block)
	if status != coreTypes.VerifyOK {
		t.Fatalf("verify fail: %v", status)
	}

	// Prepare invalid nonce tx.
	block = &coreTypes.Block{}
	block.Hash = coreCommon.NewRandomHash()
	block.Position.Height = 1
	block.ProposerID = coreTypes.NodeID{coreCommon.Hash{1, 2, 3}}
	block.Payload, block.Witness, err = prepareDataWithoutTxPool(dex, key, 1, 100)
	if err != nil {
		t.Fatalf("prepare data error: %v", err)
	}

	// Expect invalid block.
	status = dex.app.VerifyBlock(block)
	if status != coreTypes.VerifyInvalidBlock {
		t.Fatalf("verify fail: %v", status)
	}

	// Prepare invalid block height.
	block = &coreTypes.Block{}
	block.Hash = coreCommon.NewRandomHash()
	block.Position.Height = 2
	block.ProposerID = coreTypes.NodeID{coreCommon.Hash{1, 2, 3}}
	block.Payload, block.Witness, err = prepareDataWithoutTxPool(dex, key, 0, 100)
	if err != nil {
		t.Fatalf("prepare data error: %v", err)
	}

	// Expect retry later.
	status = dex.app.VerifyBlock(block)
	if status != coreTypes.VerifyRetryLater {
		t.Fatalf("verify fail expect retry later but get %v", status)
	}

	// Prepare reach block limit.
	block = &coreTypes.Block{}
	block.Hash = coreCommon.NewRandomHash()
	block.Position.Height = 1
	block.ProposerID = coreTypes.NodeID{coreCommon.Hash{1, 2, 3}}
	block.Payload, block.Witness, err = prepareDataWithoutTxPool(dex, key, 0, 10000)
	if err != nil {
		t.Fatalf("prepare data error: %v", err)
	}

	// Expect invalid block.
	status = dex.app.VerifyBlock(block)
	if status != coreTypes.VerifyInvalidBlock {
		t.Fatalf("verify fail expect invalid block but get %v", status)
	}

	// Prepare insufficient funds.
	block = &coreTypes.Block{}
	block.Hash = coreCommon.NewRandomHash()
	block.Position.Height = 1
	block.ProposerID = coreTypes.NodeID{coreCommon.Hash{1, 2, 3}}
	_, block.Witness, err = prepareDataWithoutTxPool(dex, key, 0, 0)
	if err != nil {
		t.Fatalf("prepare data error: %v", err)
	}

	signer := types.NewEIP155Signer(dex.chainConfig.ChainID)
	tx := types.NewTransaction(
		0,
		common.BytesToAddress([]byte{9}),
		big.NewInt(50000000000000001),
		params.TxGas,
		big.NewInt(10),
		nil)
	tx, err = types.SignTx(tx, signer, key)
	if err != nil {
		return
	}

	block.Payload, err = rlp.EncodeToBytes(types.Transactions{tx})
	if err != nil {
		return
	}

	// expect invalid block
	status = dex.app.VerifyBlock(block)
	if status != coreTypes.VerifyInvalidBlock {
		t.Fatalf("verify fail expect invalid block but get %v", status)
	}

	// Prepare invalid intrinsic gas.
	block = &coreTypes.Block{}
	block.Hash = coreCommon.NewRandomHash()
	block.Position.Height = 1
	block.ProposerID = coreTypes.NodeID{coreCommon.Hash{1, 2, 3}}
	_, block.Witness, err = prepareDataWithoutTxPool(dex, key, 0, 0)
	if err != nil {
		t.Fatalf("prepare data error: %v", err)
	}

	signer = types.NewEIP155Signer(dex.chainConfig.ChainID)
	tx = types.NewTransaction(
		0,
		common.BytesToAddress([]byte{9}),
		big.NewInt(1),
		params.TxGas,
		big.NewInt(10),
		make([]byte, 1))
	tx, err = types.SignTx(tx, signer, key)
	if err != nil {
		return
	}

	block.Payload, err = rlp.EncodeToBytes(types.Transactions{tx})
	if err != nil {
		return
	}

	// Expect invalid block.
	status = dex.app.VerifyBlock(block)
	if status != coreTypes.VerifyInvalidBlock {
		t.Fatalf("verify fail expect invalid block but get %v", status)
	}

	// Prepare invalid transactions with nonce.
	block = &coreTypes.Block{}
	block.Hash = coreCommon.NewRandomHash()
	block.Position.Height = 1
	block.ProposerID = coreTypes.NodeID{coreCommon.Hash{1, 2, 3}}
	_, block.Witness, err = prepareDataWithoutTxPool(dex, key, 0, 0)
	if err != nil {
		t.Fatalf("prepare data error: %v", err)
	}

	signer = types.NewEIP155Signer(dex.chainConfig.ChainID)
	tx1 := types.NewTransaction(
		0,
		common.BytesToAddress([]byte{9}),
		big.NewInt(1),
		params.TxGas,
		big.NewInt(10),
		make([]byte, 1))
	tx1, err = types.SignTx(tx, signer, key)
	if err != nil {
		return
	}

	// Invalid nonce.
	tx2 := types.NewTransaction(
		2,
		common.BytesToAddress([]byte{9}),
		big.NewInt(1),
		params.TxGas,
		big.NewInt(10),
		make([]byte, 1))
	tx2, err = types.SignTx(tx, signer, key)
	if err != nil {
		return
	}

	block.Payload, err = rlp.EncodeToBytes(types.Transactions{tx1, tx2})
	if err != nil {
		return
	}

	// Expect invalid block.
	status = dex.app.VerifyBlock(block)
	if status != coreTypes.VerifyInvalidBlock {
		t.Fatalf("verify fail expect invalid block but get %v", status)
	}

	// Invalid gas price.
	tx = types.NewTransaction(
		0,
		common.BytesToAddress([]byte{9}),
		big.NewInt(1),
		params.TxGas,
		big.NewInt(5),
		make([]byte, 1))
	tx, err = types.SignTx(tx, signer, key)
	if err != nil {
		return
	}
	block.Payload, err = rlp.EncodeToBytes(types.Transactions{tx})
	if err != nil {
		return
	}

	// Expect invalid block.
	status = dex.app.VerifyBlock(block)
	if status != coreTypes.VerifyInvalidBlock {
		t.Fatalf("verify fail expect invalid block but get %v", status)
	}

}

func TestBlockConfirmed(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("hex to ecdsa error: %v", err)
	}

	dex, err := newTestDexonWithGenesis(key)
	if err != nil {
		t.Fatalf("new test dexon error: %v", err)
	}

	chainID := big.NewInt(0)

	root := dex.blockchain.CurrentBlock().Root()
	dex.app.chainRoot.Store(uint32(chainID.Uint64()), &root)

	var (
		expectCost    big.Int
		expectNonce   uint64
		expectCounter uint64
	)
	for i := 0; i < 10; i++ {
		var startNonce int
		if expectNonce != 0 {
			startNonce = int(expectNonce) + 1
		}
		payload, witness, cost, nonce, err := prepareData(dex, key, startNonce, 50)
		if err != nil {
			t.Fatalf("prepare data error: %v", err)
		}
		expectCost.Add(&expectCost, &cost)
		expectNonce = nonce

		block := &coreTypes.Block{}
		block.Hash = coreCommon.NewRandomHash()
		block.Witness = witness
		block.Payload = payload
		block.ProposerID = coreTypes.NodeID{coreCommon.Hash{1, 2, 3}}

		dex.app.BlockConfirmed(*block)
		expectCounter++
	}

	info := dex.app.blockchain.GetAddressInfo(uint32(chainID.Uint64()),
		crypto.PubkeyToAddress(key.PublicKey))

	if info.Counter != expectCounter {
		t.Fatalf("expect address counter is %v but %v", expectCounter, info.Counter)
	}

	if info.Cost.Cmp(&expectCost) != 0 {
		t.Fatalf("expect address cost is %v but %v", expectCost.Uint64(), info.Cost.Uint64())
	}

	if info.Nonce != expectNonce {
		t.Fatalf("expect address nonce is %v but %v", expectNonce, info.Nonce)
	}
}

func TestBlockDelivered(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("hex to ecdsa error: %v", err)
	}

	dex, err := newTestDexonWithGenesis(key)
	if err != nil {
		t.Fatalf("new test dexon error: %v", err)
	}

	chainID := big.NewInt(0)

	root := dex.blockchain.CurrentBlock().Root()
	dex.app.chainRoot.Store(uint32(chainID.Uint64()), &root)

	address := crypto.PubkeyToAddress(key.PublicKey)
	firstBlocksInfo, err := prepareConfirmedBlocks(dex, []*ecdsa.PrivateKey{key}, 50)
	if err != nil {
		t.Fatalf("preapare confirmed block error: %v", err)
	}

	dex.app.BlockDelivered(firstBlocksInfo[0].Block.Hash, firstBlocksInfo[0].Block.Position,
		coreTypes.FinalizationResult{
			Timestamp: time.Now(),
			Height:    1,
		})

	currentState, err := dex.blockchain.State()
	if err != nil {
		t.Fatalf("get state error: %v", err)
	}
	currentBlock := dex.blockchain.CurrentBlock()
	if currentBlock.NumberU64() != 1 {
		t.Fatalf("unexpected current block number %v", currentBlock.NumberU64())
	}

	currentNonce := currentState.GetNonce(address)
	if currentNonce != firstBlocksInfo[0].Nonce+1 {
		t.Fatalf("unexpected pending state nonce %v", currentNonce)
	}

	balance := currentState.GetBalance(address)
	if new(big.Int).Add(balance, &firstBlocksInfo[0].Cost).Cmp(big.NewInt(50000000000000000)) != 0 {
		t.Fatalf("unexpected pending state balance %v", balance)
	}
}

func BenchmarkBlockDeliveredFlow(b *testing.B) {
	key, err := crypto.GenerateKey()
	if err != nil {
		b.Fatalf("hex to ecdsa error: %v", err)
		return
	}

	dex, err := newTestDexonWithGenesis(key)
	if err != nil {
		b.Fatalf("new test dexon error: %v", err)
	}

	b.ResetTimer()
	for i := 1; i <= b.N; i++ {
		blocksInfo, err := prepareConfirmedBlocks(dex, []*ecdsa.PrivateKey{key}, 100)
		if err != nil {
			b.Fatalf("preapare confirmed block error: %v", err)
			return
		}

		dex.app.BlockDelivered(blocksInfo[0].Block.Hash, blocksInfo[0].Block.Position,
			coreTypes.FinalizationResult{
				Timestamp: time.Now(),
				Height:    uint64(i),
			})
	}
}

func newTestDexonWithGenesis(allocKey *ecdsa.PrivateKey) (*Dexon, error) {
	db := ethdb.NewMemDatabase()

	key, err := crypto.GenerateKey()
	if err != nil {
		panic(err)
	}

	testBankAddress := crypto.PubkeyToAddress(allocKey.PublicKey)
	genesis := core.DefaultTestnetGenesisBlock()
	genesis.Alloc = core.GenesisAlloc{
		testBankAddress: {
			Balance:   big.NewInt(100000000000000000),
			Staked:    big.NewInt(50000000000000000),
			PublicKey: crypto.FromECDSAPub(&key.PublicKey),
		},
	}
	genesis.Config.Dexcon.MinGasPrice = big.NewInt(10)
	chainConfig, _, err := core.SetupGenesisBlock(db, genesis)
	if err != nil {
		return nil, err
	}

	config := Config{PrivateKey: key}
	vmConfig := vm.Config{IsBlockProposer: true}

	engine := dexcon.New()

	dex := &Dexon{
		chainDb:     db,
		chainConfig: chainConfig,
		networkID:   config.NetworkId,
		engine:      engine,
	}

	dex.blockchain, err = core.NewBlockChain(db, nil, chainConfig, engine, vmConfig, nil)
	if err != nil {
		return nil, err
	}

	txPoolConfig := core.DefaultTxPoolConfig
	dex.txPool = core.NewTxPool(txPoolConfig, chainConfig, dex.blockchain, true)

	dex.APIBackend = &DexAPIBackend{dex, nil}
	dex.governance = NewDexconGovernance(dex.APIBackend, dex.chainConfig, config.PrivateKey)
	engine.SetGovStateFetcher(dex.governance)
	dex.app = NewDexconApp(dex.txPool, dex.blockchain, dex.governance, db, &config)

	return dex, nil
}

// Add tx into tx pool.
func addTx(dex *Dexon, nonce int, signer types.Signer, key *ecdsa.PrivateKey) (
	*types.Transaction, error) {
	tx := types.NewTransaction(
		uint64(nonce),
		common.BytesToAddress([]byte{9}),
		big.NewInt(int64(nonce*1)),
		params.TxGas,
		big.NewInt(10),
		nil)
	tx, err := types.SignTx(tx, signer, key)
	if err != nil {
		return nil, err
	}

	if err := dex.txPool.AddRemote(tx); err != nil {
		return nil, err
	}

	return tx, nil
}

// Prepare data with given transaction number and start nonce.
func prepareData(dex *Dexon, key *ecdsa.PrivateKey, startNonce, txNum int) (
	payload []byte, witness coreTypes.Witness, cost big.Int, nonce uint64, err error) {
	signer := types.NewEIP155Signer(dex.chainConfig.ChainID)

	for n := startNonce; n < startNonce+txNum; n++ {
		var tx *types.Transaction
		tx, err = addTx(dex, n, signer, key)
		if err != nil {
			return
		}

		cost.Add(&cost, tx.Cost())
		nonce = uint64(n)
	}

	payload, err = dex.app.PreparePayload(coreTypes.Position{})
	if err != nil {
		return
	}

	witness, err = dex.app.PrepareWitness(0)
	if err != nil {
		return
	}

	return
}

func prepareConfirmedBlockWithTxAndData(dex *Dexon, key *ecdsa.PrivateKey, data [][]byte, round uint64) (
	Block *coreTypes.Block, err error) {
	address := crypto.PubkeyToAddress(key.PublicKey)

	for _, d := range data {
		// Prepare one block for pending.
		nonce := dex.txPool.State().GetNonce(address)
		signer := types.NewEIP155Signer(dex.chainConfig.ChainID)
		tx := types.NewTransaction(uint64(nonce), vm.GovernanceContractAddress, big.NewInt(0), params.TxGas*2,
			big.NewInt(1), d)
		tx, err = types.SignTx(tx, signer, key)
		if err != nil {
			return nil, err
		}

		dex.txPool.AddRemote(tx)
		if err != nil {
			return nil, err
		}
	}
	var (
		payload []byte
		witness coreTypes.Witness
	)

	payload, err = dex.app.PreparePayload(coreTypes.Position{Round: round})
	if err != nil {
		return
	}

	witness, err = dex.app.PrepareWitness(0)
	if err != nil {
		return nil, err
	}

	block := &coreTypes.Block{}
	block.Hash = coreCommon.NewRandomHash()
	block.Witness = witness
	block.Payload = payload
	block.Position.Round = round
	block.ProposerID = coreTypes.NodeID{coreCommon.Hash{1, 2, 3}}

	status := dex.app.VerifyBlock(block)
	if status != coreTypes.VerifyOK {
		err = fmt.Errorf("verify fail: %v", status)
		return nil, err
	}

	dex.app.BlockConfirmed(*block)
	return block, nil
}

func prepareDataWithoutTxPool(dex *Dexon, key *ecdsa.PrivateKey, startNonce, txNum int) (
	payload []byte, witness coreTypes.Witness, err error) {
	signer := types.NewEIP155Signer(dex.chainConfig.ChainID)

	var transactions types.Transactions
	for n := startNonce; n < startNonce+txNum; n++ {
		tx := types.NewTransaction(
			uint64(n),
			common.BytesToAddress([]byte{9}),
			big.NewInt(int64(n*1)),
			params.TxGas,
			big.NewInt(10),
			nil)
		tx, err = types.SignTx(tx, signer, key)
		if err != nil {
			return
		}
		transactions = append(transactions, tx)
	}

	payload, err = rlp.EncodeToBytes(&transactions)
	if err != nil {
		return
	}

	witness, err = dex.app.PrepareWitness(0)
	if err != nil {
		return
	}

	return
}

func prepareConfirmedBlocks(dex *Dexon, keys []*ecdsa.PrivateKey, txNum int) (blocksInfo []struct {
	Block *coreTypes.Block
	Cost  big.Int
	Nonce uint64
}, err error) {
	for _, key := range keys {
		address := crypto.PubkeyToAddress(key.PublicKey)

		// Prepare one block for pending.
		var (
			payload []byte
			witness coreTypes.Witness
			cost    big.Int
			nonce   uint64
		)
		startNonce := dex.txPool.State().GetNonce(address)
		payload, witness, cost, nonce, err = prepareData(dex, key, int(startNonce), txNum)
		if err != nil {
			err = fmt.Errorf("prepare data error: %v", err)
			return
		}

		block := &coreTypes.Block{}
		block.Hash = coreCommon.NewRandomHash()
		block.Witness = witness
		block.Payload = payload
		block.ProposerID = coreTypes.NodeID{coreCommon.Hash{1, 2, 3}}

		status := dex.app.VerifyBlock(block)
		if status != coreTypes.VerifyOK {
			err = fmt.Errorf("verify fail: %v", status)
			return
		}

		dex.app.BlockConfirmed(*block)

		blocksInfo = append(blocksInfo, struct {
			Block *coreTypes.Block
			Cost  big.Int
			Nonce uint64
		}{Block: block, Cost: cost, Nonce: nonce})
	}

	return
}
