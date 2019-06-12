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

package vm

import (
	"bytes"
	"crypto/ecdsa"
	"math/big"
	"math/rand"
	"sort"
	"testing"
	"time"

	coreCommon "github.com/dexon-foundation/dexon-consensus/common"
	coreCrypto "github.com/dexon-foundation/dexon-consensus/core/crypto"
	coreEcdsa "github.com/dexon-foundation/dexon-consensus/core/crypto/ecdsa"
	coreTypes "github.com/dexon-foundation/dexon-consensus/core/types"
	coreUtils "github.com/dexon-foundation/dexon-consensus/core/utils"

	"github.com/dexon-foundation/dexon/common"
	"github.com/dexon-foundation/dexon/core/state"
	"github.com/dexon-foundation/dexon/crypto"
	"github.com/dexon-foundation/dexon/ethdb"
	"github.com/dexon-foundation/dexon/params"
	"github.com/dexon-foundation/dexon/rlp"
	"github.com/stretchr/testify/suite"
)

func init() {
	rand.Seed(time.Now().UnixNano())
}

func randomBytes(minLength, maxLength int32) []byte {
	length := rand.Int31()%(maxLength-minLength) + minLength
	b := make([]byte, length)
	for i := range b {
		b[i] = byte(65 + rand.Int31()%60)
	}
	return b
}

type GovernanceStateHelperTestSuite struct {
	suite.Suite

	s *GovernanceStateHelper
}

func (g *GovernanceStateHelperTestSuite) SetupTest() {
	db := state.NewDatabase(ethdb.NewMemDatabase())
	statedb, err := state.New(common.Hash{}, db)
	if err != nil {
		panic(err)
	}
	g.s = &GovernanceStateHelper{statedb}
}

func (g *GovernanceStateHelperTestSuite) TestReadWriteBytes() {
	for i := 0; i < 100; i++ {
		// Short bytes.
		loc := big.NewInt(rand.Int63())
		data := randomBytes(3, 32)
		g.s.writeBytes(loc, data)
		read := g.s.readBytes(loc)
		g.Require().Equal(0, bytes.Compare(data, read))

		// long bytes.
		loc = big.NewInt(rand.Int63())
		data = randomBytes(33, 2560)
		g.s.writeBytes(loc, data)
		read = g.s.readBytes(loc)
		g.Require().Equal(0, bytes.Compare(data, read))
	}
}

func TestGovernanceStateHelper(t *testing.T) {
	suite.Run(t, new(GovernanceStateHelperTestSuite))
}

type GovernanceContractTestSuite struct {
	suite.Suite

	config  *params.DexconConfig
	memDB   *ethdb.MemDatabase
	stateDB *state.StateDB
	s       *GovernanceStateHelper
}

func (g *GovernanceContractTestSuite) SetupTest() {
	memDB := ethdb.NewMemDatabase()
	stateDB, err := state.New(common.Hash{}, state.NewDatabase(memDB))
	if err != nil {
		panic(err)
	}
	g.memDB = memDB
	g.stateDB = stateDB
	g.s = &GovernanceStateHelper{stateDB}

	config := params.TestnetChainConfig.Dexcon
	config.LockupPeriod = 1000
	config.NextHalvingSupply = new(big.Int).Mul(big.NewInt(1e18), big.NewInt(2.5e9))
	config.LastHalvedAmount = new(big.Int).Mul(big.NewInt(1e18), big.NewInt(1.5e9))
	config.MiningVelocity = 0.1875

	g.config = config

	// Give governance contract balance so it will not be deleted because of being an empty state object.
	stateDB.AddBalance(GovernanceContractAddress, big.NewInt(1))

	// Genesis CRS.
	crs := crypto.Keccak256Hash([]byte(config.GenesisCRSText))
	g.s.PushCRS(crs)

	// Round 0 height.
	g.s.PushRoundHeight(big.NewInt(0))

	// Owner.
	g.s.SetOwner(g.config.Owner)

	// Governance configuration.
	g.s.UpdateConfiguration(config)

	g.stateDB.Commit(true)
}

func (g *GovernanceContractTestSuite) newPrefundAccount() (*ecdsa.PrivateKey, common.Address) {
	privKey, err := crypto.GenerateKey()
	if err != nil {
		panic(err)
	}
	address := crypto.PubkeyToAddress(privKey.PublicKey)

	g.stateDB.AddBalance(address, new(big.Int).Mul(big.NewInt(1e18), big.NewInt(2e6)))
	return privKey, address
}

func (g *GovernanceContractTestSuite) call(caller common.Address, input []byte, value *big.Int) ([]byte, error) {
	context := Context{
		CanTransfer: func(db StateDB, addr common.Address, amount *big.Int) bool {
			return db.GetBalance(addr).Cmp(amount) >= 0
		},
		Transfer: func(db StateDB, sender common.Address, recipient common.Address, amount *big.Int) {
			db.SubBalance(sender, amount)
			db.AddBalance(recipient, amount)
		},
		GetRoundHeight: func(round uint64) (uint64, bool) {
			switch round {
			case 0:
				return 0, true
			case 1:
				return 1000, true
			case 2:
				return 2000, true
			}
			return 0, false
		},
		Time:        big.NewInt(time.Now().UnixNano() / 1000000),
		BlockNumber: big.NewInt(0),
	}

	evm := NewEVM(context, g.stateDB, params.TestChainConfig, Config{IsBlockProposer: true})
	ret, _, err := evm.Call(AccountRef(caller), GovernanceContractAddress, input, 10000000, value)
	return ret, err
}

func (g *GovernanceContractTestSuite) TestTransferOwnership() {
	_, addr := g.newPrefundAccount()

	input, err := abiObject.Pack("transferOwnership", addr)
	g.Require().NoError(err)

	// Call with non-owner.
	_, err = g.call(addr, input, big.NewInt(0))
	g.Require().NotNil(err)

	// Call with owner.
	_, err = g.call(g.config.Owner, input, big.NewInt(0))
	g.Require().NoError(err)
	g.Require().Equal(addr, g.s.Owner())
}

func (g *GovernanceContractTestSuite) TestStakeUnstakeWithoutExtraDelegators() {
	privKey, addr := g.newPrefundAccount()
	pk := crypto.FromECDSAPub(&privKey.PublicKey)

	// Stake.
	amount := new(big.Int).Mul(big.NewInt(1e18), big.NewInt(1e6))
	balanceBeforeStake := g.stateDB.GetBalance(addr)
	input, err := abiObject.Pack("stake", pk, "Test1", "test1@dexon.org", "Taipei, Taiwan", "https://dexon.org")
	g.Require().NoError(err)
	_, err = g.call(addr, input, amount)
	g.Require().NoError(err)

	g.Require().Equal(1, int(g.s.LenNodes().Uint64()))
	g.Require().Equal(1, len(g.s.QualifiedNodes()))
	g.Require().Equal("Test1", g.s.Node(big.NewInt(0)).Name)
	g.Require().Equal(amount.String(), g.s.TotalStaked().String())

	// Check balance.
	g.Require().Equal(new(big.Int).Sub(balanceBeforeStake, amount), g.stateDB.GetBalance(addr))
	g.Require().Equal(new(big.Int).Add(big.NewInt(1), amount), g.stateDB.GetBalance(GovernanceContractAddress))

	// Staking again should fail.
	_, err = g.call(addr, input, amount)
	g.Require().NotNil(err)

	// Unstake.
	input, err = abiObject.Pack("unstake")
	g.Require().NoError(err)
	_, err = g.call(addr, input, big.NewInt(0))
	g.Require().NoError(err)
	g.Require().Equal(0, len(g.s.QualifiedNodes()))
	g.Require().Equal(1, int(g.s.LenDelegators(addr).Uint64()))
	g.Require().Equal(1, int(g.s.LenNodes().Uint64()))

	node := g.s.Node(big.NewInt(0))
	g.Require().Equal(big.NewInt(0).String(), node.Staked.String())
	g.Require().Equal(big.NewInt(0).String(), g.s.TotalStaked().String())

	// Wait for lockup time than withdraw.
	time.Sleep(time.Second * 2)
	input, err = abiObject.Pack("withdraw", addr)
	g.Require().NoError(err)
	_, err = g.call(addr, input, big.NewInt(0))
	g.Require().NoError(err)

	g.Require().Equal(0, len(g.s.QualifiedNodes()))
	g.Require().Equal(0, int(g.s.LenDelegators(addr).Uint64()))
	g.Require().Equal(0, int(g.s.LenNodes().Uint64()))

	// Stake 2 nodes, and unstake the first then the second.

	// 2nd node Stake.
	privKey2, addr2 := g.newPrefundAccount()
	pk2 := crypto.FromECDSAPub(&privKey2.PublicKey)
	input, err = abiObject.Pack("stake", pk2, "Test2", "test2@dexon.org", "Taipei, Taiwan", "https://dexon.org")
	g.Require().NoError(err)
	_, err = g.call(addr2, input, amount)
	g.Require().NoError(err)
	g.Require().Equal("Test2", g.s.Node(big.NewInt(0)).Name)
	g.Require().Equal(0, int(g.s.NodesOffsetByAddress(addr2).Int64()))

	// 1st node Stake.
	input, err = abiObject.Pack("stake", pk, "Test1", "test1@dexon.org", "Taipei, Taiwan", "https://dexon.org")
	g.Require().NoError(err)
	_, err = g.call(addr, input, amount)
	g.Require().NoError(err)

	g.Require().Equal(2, len(g.s.QualifiedNodes()))
	g.Require().Equal(new(big.Int).Mul(amount, big.NewInt(2)).String(), g.s.TotalStaked().String())

	// 2nd node Unstake.
	input, err = abiObject.Pack("unstake")
	g.Require().NoError(err)
	_, err = g.call(addr2, input, big.NewInt(0))
	g.Require().NoError(err)
	node = g.s.Node(big.NewInt(0))
	g.Require().Equal("Test2", node.Name)
	g.Require().Equal(big.NewInt(0).String(), node.Staked.String())
	g.Require().Equal(1, len(g.s.QualifiedNodes()))
	g.Require().Equal(amount.String(), g.s.TotalStaked().String())

	time.Sleep(time.Second * 2)
	input, err = abiObject.Pack("withdraw", addr2)
	g.Require().NoError(err)
	_, err = g.call(addr2, input, big.NewInt(0))
	g.Require().NoError(err)
	g.Require().Equal(1, len(g.s.QualifiedNodes()))

	g.Require().Equal(1, int(g.s.LenNodes().Uint64()))
	g.Require().Equal("Test1", g.s.Node(big.NewInt(0)).Name)
	g.Require().Equal(-1, int(g.s.NodesOffsetByAddress(addr2).Int64()))

	// 1st node Unstake.
	input, err = abiObject.Pack("unstake")
	g.Require().NoError(err)
	_, err = g.call(addr, input, big.NewInt(0))
	g.Require().NoError(err)
	g.Require().Equal(0, len(g.s.QualifiedNodes()))
	g.Require().Equal(big.NewInt(0).String(), g.s.TotalStaked().String())

	time.Sleep(time.Second * 2)
	input, err = abiObject.Pack("withdraw", addr)
	g.Require().NoError(err)
	_, err = g.call(addr, input, big.NewInt(0))
	g.Require().NoError(err)

	g.Require().Equal(0, int(g.s.LenNodes().Uint64()))
	g.Require().Equal(-1, int(g.s.NodesOffsetByAddress(addr).Int64()))
	g.Require().Equal(-1, int(g.s.DelegatorsOffset(addr, addr).Int64()))

	// Check balance.
	g.Require().Equal(balanceBeforeStake, g.stateDB.GetBalance(addr))
	g.Require().Equal(balanceBeforeStake, g.stateDB.GetBalance(addr2))
	g.Require().Equal(big.NewInt(1), g.stateDB.GetBalance(GovernanceContractAddress))
}

func (g *GovernanceContractTestSuite) TestDelegateUndelegate() {
	privKey, addr := g.newPrefundAccount()
	pk := crypto.FromECDSAPub(&privKey.PublicKey)

	// Stake.
	input, err := abiObject.Pack("stake", pk, "Test1", "test1@dexon.org", "Taipei, Taiwan", "https://dexon.org")
	g.Require().NoError(err)
	ownerStaked := new(big.Int).Mul(big.NewInt(1e18), big.NewInt(5e5))
	_, err = g.call(addr, input, ownerStaked)
	g.Require().NoError(err)
	g.Require().Equal(0, len(g.s.QualifiedNodes()))
	g.Require().Equal(addr, g.s.Delegator(addr, big.NewInt(0)).Owner)
	g.Require().Equal(ownerStaked, g.s.Node(big.NewInt(0)).Staked)

	// 1st delegator delegate to 1st node.
	_, addrDelegator := g.newPrefundAccount()

	balanceBeforeDelegate := g.stateDB.GetBalance(addrDelegator)
	amount := new(big.Int).Mul(big.NewInt(1e18), big.NewInt(3e5))
	input, err = abiObject.Pack("delegate", addr)
	g.Require().NoError(err)

	_, err = g.call(addrDelegator, input, amount)
	g.Require().NoError(err)
	g.Require().Equal(new(big.Int).Sub(balanceBeforeDelegate, amount), g.stateDB.GetBalance(addrDelegator))
	g.Require().Equal(addrDelegator, g.s.Delegator(addr, big.NewInt(1)).Owner)
	g.Require().Equal(new(big.Int).Add(amount, ownerStaked), g.s.Node(big.NewInt(0)).Staked)
	g.Require().Equal(g.s.Node(big.NewInt(0)).Staked.String(), g.s.TotalStaked().String())
	g.Require().Equal(1, int(g.s.DelegatorsOffset(addr, addrDelegator).Int64()))

	// Same person delegate the 2nd time should fail.
	_, err = g.call(addrDelegator, input, big.NewInt(1e18))
	g.Require().NotNil(err)

	// Not yet qualified.
	g.Require().Equal(0, len(g.s.QualifiedNodes()))

	// 2nd delegator delegate to 1st node.
	_, addrDelegator2 := g.newPrefundAccount()
	_, err = g.call(addrDelegator2, input, amount)
	g.Require().NoError(err)
	g.Require().Equal(new(big.Int).Sub(balanceBeforeDelegate, amount), g.stateDB.GetBalance(addrDelegator2))
	g.Require().Equal(addrDelegator2, g.s.Delegator(addr, big.NewInt(2)).Owner)
	g.Require().Equal(new(big.Int).Add(ownerStaked, new(big.Int).Mul(amount, big.NewInt(2))),
		g.s.Node(big.NewInt(0)).Staked)
	g.Require().Equal(g.s.Node(big.NewInt(0)).Staked.String(), g.s.TotalStaked().String())
	g.Require().Equal(2, int(g.s.DelegatorsOffset(addr, addrDelegator2).Int64()))

	// Qualified.
	g.Require().Equal(1, len(g.s.QualifiedNodes()))

	// Undelegate addrDelegator.
	balanceBeforeUnDelegate := g.stateDB.GetBalance(addrDelegator)
	input, err = abiObject.Pack("undelegate", addr)
	g.Require().NoError(err)
	_, err = g.call(addrDelegator, input, big.NewInt(0))
	g.Require().Equal(new(big.Int).Add(amount, ownerStaked), g.s.Node(big.NewInt(0)).Staked)
	g.Require().Equal(g.s.Node(big.NewInt(0)).Staked.String(), g.s.TotalStaked().String())
	g.Require().NoError(err)

	// Undelegate the second time should fail.
	_, err = g.call(addrDelegator, input, big.NewInt(0))
	g.Require().Error(err)

	// Withdraw within lockup time should fail.
	input, err = abiObject.Pack("withdraw", addr)
	g.Require().NoError(err)
	_, err = g.call(addrDelegator, input, big.NewInt(0))
	g.Require().NotNil(err)

	g.Require().Equal(3, int(g.s.LenDelegators(addr).Uint64()))
	g.Require().Equal(balanceBeforeUnDelegate, g.stateDB.GetBalance(addrDelegator))
	g.Require().NotEqual(-1, int(g.s.DelegatorsOffset(addr, addrDelegator).Int64()))

	// Wait for lockup time than withdraw.
	time.Sleep(time.Second * 2)
	input, err = abiObject.Pack("withdraw", addr)
	g.Require().NoError(err)
	_, err = g.call(addrDelegator, input, big.NewInt(0))
	g.Require().NoError(err)

	g.Require().Equal(2, int(g.s.LenDelegators(addr).Uint64()))
	g.Require().Equal(new(big.Int).Add(balanceBeforeUnDelegate, amount), g.stateDB.GetBalance(addrDelegator))
	g.Require().Equal(-1, int(g.s.DelegatorsOffset(addr, addrDelegator).Int64()))

	// Withdraw when their is no delegation should fail.
	time.Sleep(time.Second)
	input, err = abiObject.Pack("withdraw", addr)
	g.Require().NoError(err)
	_, err = g.call(addrDelegator, input, big.NewInt(0))
	g.Require().Error(err)

	// Undelegate addrDelegator2.
	balanceBeforeUnDelegate = g.stateDB.GetBalance(addrDelegator2)
	input, err = abiObject.Pack("undelegate", addr)
	g.Require().NoError(err)
	_, err = g.call(addrDelegator2, input, big.NewInt(0))
	g.Require().NoError(err)

	g.Require().Equal(ownerStaked, g.s.Node(big.NewInt(0)).Staked)
	g.Require().Equal(g.s.Node(big.NewInt(0)).Staked.String(), g.s.TotalStaked().String())

	// Wait for lockup time than withdraw.
	time.Sleep(time.Second * 2)
	input, err = abiObject.Pack("withdraw", addr)
	g.Require().NoError(err)
	_, err = g.call(addrDelegator2, input, big.NewInt(0))
	g.Require().NoError(err)

	g.Require().Equal(1, int(g.s.LenDelegators(addr).Uint64()))
	g.Require().Equal(new(big.Int).Add(balanceBeforeUnDelegate, amount), g.stateDB.GetBalance(addrDelegator2))
	g.Require().Equal(-1, int(g.s.DelegatorsOffset(addr, addrDelegator2).Int64()))

	// Unqualified
	g.Require().Equal(0, len(g.s.QualifiedNodes()))

	// Owner undelegate itself.
	g.Require().Equal(1, int(g.s.LenNodes().Uint64()))
	g.Require().Equal(1, int(g.s.LenDelegators(addr).Uint64()))

	input, err = abiObject.Pack("undelegate", addr)
	g.Require().NoError(err)
	_, err = g.call(addr, input, big.NewInt(0))
	g.Require().NoError(err)
	g.Require().Equal(big.NewInt(0).String(), g.s.Node(big.NewInt(0)).Staked.String())
	g.Require().Equal(big.NewInt(0).String(), g.s.TotalStaked().String())

	time.Sleep(time.Second * 2)
	input, err = abiObject.Pack("withdraw", addr)
	g.Require().NoError(err)
	_, err = g.call(addr, input, big.NewInt(0))

	g.Require().Equal(0, int(g.s.LenNodes().Uint64()))
	g.Require().Equal(0, int(g.s.LenDelegators(addr).Uint64()))
}

func (g *GovernanceContractTestSuite) TestFine() {
	privKey, addr := g.newPrefundAccount()
	pk := crypto.FromECDSAPub(&privKey.PublicKey)

	// Stake.
	input, err := abiObject.Pack("stake", pk, "Test1", "test1@dexon.org", "Taipei, Taiwan", "https://dexon.org")
	g.Require().NoError(err)
	ownerStaked := new(big.Int).Mul(big.NewInt(1e18), big.NewInt(5e5))
	_, err = g.call(addr, input, ownerStaked)
	g.Require().NoError(err)
	g.Require().Equal(0, len(g.s.QualifiedNodes()))
	g.Require().Equal(addr, g.s.Delegator(addr, big.NewInt(0)).Owner)
	g.Require().Equal(ownerStaked, g.s.Node(big.NewInt(0)).Staked)

	// 1st delegator delegate to 1st node.
	_, addrDelegator := g.newPrefundAccount()

	balanceBeforeDelegate := g.stateDB.GetBalance(addrDelegator)
	amount := new(big.Int).Mul(big.NewInt(1e18), big.NewInt(5e5))
	input, err = abiObject.Pack("delegate", addr)
	g.Require().NoError(err)

	_, err = g.call(addrDelegator, input, amount)
	g.Require().NoError(err)
	g.Require().Equal(new(big.Int).Sub(balanceBeforeDelegate, amount), g.stateDB.GetBalance(addrDelegator))
	g.Require().Equal(addrDelegator, g.s.Delegator(addr, big.NewInt(1)).Owner)
	g.Require().Equal(new(big.Int).Add(amount, ownerStaked), g.s.Node(big.NewInt(0)).Staked)
	g.Require().Equal(1, int(g.s.DelegatorsOffset(addr, addrDelegator).Int64()))

	// Qualified.
	g.Require().Equal(1, len(g.s.QualifiedNodes()))

	// Paying to node without fine should fail.
	input, err = abiObject.Pack("payFine", addr)
	g.Require().NoError(err)
	_, err = g.call(addrDelegator, input, amount)
	g.Require().NotNil(err)

	// Fined.
	offset := g.s.NodesOffsetByAddress(addr)
	g.Require().True(offset.Cmp(big.NewInt(0)) >= 0)
	node := g.s.Node(offset)
	node.Fined = new(big.Int).Set(amount)
	g.s.UpdateNode(offset, node)
	node = g.s.Node(offset)
	g.Require().Equal(0, node.Fined.Cmp(amount))

	// Not qualified after fined.
	g.Require().Equal(0, len(g.s.QualifiedNodes()))

	// Cannot undelegate before fines are paied.
	input, err = abiObject.Pack("undelegate", addr)
	g.Require().NoError(err)
	_, err = g.call(addrDelegator, input, big.NewInt(0))
	g.Require().NotNil(err)

	// Only delegators can pay fine.
	_, addrDelegator2 := g.newPrefundAccount()
	input, err = abiObject.Pack("payFine", addr)
	g.Require().NoError(err)
	_, err = g.call(addrDelegator2, input, big.NewInt(5e5))
	g.Require().NotNil(err)

	// Paying more than fine should fail.
	payAmount := new(big.Int).Add(amount, amount)
	input, err = abiObject.Pack("payFine", addr)
	g.Require().NoError(err)
	_, err = g.call(addrDelegator, input, payAmount)
	g.Require().NotNil(err)

	// Pay the fine.
	input, err = abiObject.Pack("payFine", addr)
	g.Require().NoError(err)
	_, err = g.call(addrDelegator, input, amount)
	g.Require().NoError(err)

	// Qualified.
	g.Require().Equal(1, len(g.s.QualifiedNodes()))

	// Can undelegate after all fines are paied.
	input, err = abiObject.Pack("undelegate", addr)
	g.Require().NoError(err)
	_, err = g.call(addrDelegator, input, big.NewInt(0))
	g.Require().NoError(err)
}

func (g *GovernanceContractTestSuite) TestUnstakeWithExtraDelegators() {
	privKey, addr := g.newPrefundAccount()
	pk := crypto.FromECDSAPub(&privKey.PublicKey)

	// Stake.
	amount := new(big.Int).Mul(big.NewInt(1e18), big.NewInt(5e5))
	input, err := abiObject.Pack("stake", pk, "Test1", "test1@dexon.org", "Taipei, Taiwan", "https://dexon.org")
	g.Require().NoError(err)
	_, err = g.call(addr, input, amount)
	g.Require().NoError(err)

	// 1st delegator delegate to 1st node.
	_, addrDelegator := g.newPrefundAccount()

	balanceBeforeDelegate := g.stateDB.GetBalance(addrDelegator)
	amount = new(big.Int).Mul(big.NewInt(1e18), big.NewInt(3e5))
	input, err = abiObject.Pack("delegate", addr)
	g.Require().NoError(err)

	_, err = g.call(addrDelegator, input, amount)
	g.Require().NoError(err)
	g.Require().Equal(new(big.Int).Sub(balanceBeforeDelegate, amount), g.stateDB.GetBalance(addrDelegator))
	g.Require().Equal(addrDelegator, g.s.Delegator(addr, big.NewInt(1)).Owner)
	g.Require().Equal(0, len(g.s.QualifiedNodes()))

	// 2st delegator delegate to 1st node.
	_, addrDelegator2 := g.newPrefundAccount()

	balanceBeforeDelegate = g.stateDB.GetBalance(addrDelegator2)
	input, err = abiObject.Pack("delegate", addr)
	g.Require().NoError(err)

	_, err = g.call(addrDelegator2, input, amount)
	g.Require().NoError(err)
	g.Require().Equal(new(big.Int).Sub(balanceBeforeDelegate, amount), g.stateDB.GetBalance(addrDelegator2))
	g.Require().Equal(addrDelegator2, g.s.Delegator(addr, big.NewInt(2)).Owner)

	// Node is now qualified.
	g.Require().Equal(1, len(g.s.QualifiedNodes()))

	// Unstake.
	input, err = abiObject.Pack("unstake")
	g.Require().NoError(err)
	_, err = g.call(addr, input, big.NewInt(0))
	g.Require().NoError(err)

	time.Sleep(time.Second * 2)
	input, err = abiObject.Pack("withdraw", addr)
	g.Require().NoError(err)
	_, err = g.call(addr, input, big.NewInt(0))
	g.Require().NoError(err)
	_, err = g.call(addrDelegator, input, big.NewInt(0))
	g.Require().NoError(err)
	_, err = g.call(addrDelegator2, input, big.NewInt(0))
	g.Require().NoError(err)

	g.Require().Equal(0, int(g.s.LenDelegators(addr).Uint64()))
	g.Require().Equal(0, int(g.s.LenNodes().Uint64()))

	// Check balance.
	g.Require().Equal(balanceBeforeDelegate, g.stateDB.GetBalance(addr))
	g.Require().Equal(balanceBeforeDelegate, g.stateDB.GetBalance(addrDelegator))
	g.Require().Equal(balanceBeforeDelegate, g.stateDB.GetBalance(addrDelegator2))
	g.Require().Equal(big.NewInt(1), g.stateDB.GetBalance(GovernanceContractAddress))
}

func (g *GovernanceContractTestSuite) TestUpdateConfiguration() {
	_, addr := g.newPrefundAccount()

	input, err := abiObject.Pack("updateConfiguration",
		new(big.Int).Mul(big.NewInt(1e18), big.NewInt(1e6)),
		big.NewInt(1000),
		big.NewInt(8000000),
		big.NewInt(6),
		big.NewInt(250),
		big.NewInt(2500),
		big.NewInt(0),
		big.NewInt(667000),
		big.NewInt(4),
		big.NewInt(4),
		big.NewInt(600000),
		big.NewInt(900),
		[]*big.Int{big.NewInt(1), big.NewInt(1), big.NewInt(1)})
	g.Require().NoError(err)

	// Call with non-owner.
	_, err = g.call(addr, input, big.NewInt(0))
	g.Require().NotNil(err)

	// Call with owner.
	_, err = g.call(g.config.Owner, input, big.NewInt(0))
	g.Require().NoError(err)
}

func (g *GovernanceContractTestSuite) TestSnapshotRound() {
	_, addr := g.newPrefundAccount()

	// Wrong height.
	input, err := abiObject.Pack("snapshotRound", big.NewInt(1), big.NewInt(666))
	g.Require().NoError(err)
	_, err = g.call(addr, input, big.NewInt(0))
	g.Require().NotNil(err)

	// Invalid round.
	input, err = abiObject.Pack("snapshotRound", big.NewInt(2), big.NewInt(2000))
	g.Require().NoError(err)
	_, err = g.call(addr, input, big.NewInt(0))
	g.Require().NotNil(err)

	// Correct.
	input, err = abiObject.Pack("snapshotRound", big.NewInt(1), big.NewInt(1000))
	g.Require().NoError(err)
	_, err = g.call(addr, input, big.NewInt(0))
	g.Require().NoError(err)

	// Duplicate round.
	input, err = abiObject.Pack("snapshotRound", big.NewInt(1), big.NewInt(1000))
	g.Require().NoError(err)
	_, err = g.call(addr, input, big.NewInt(0))
	g.Require().NotNil(err)

	// Invalid round.
	input, err = abiObject.Pack("snapshotRound", big.NewInt(3), big.NewInt(3000))
	g.Require().NoError(err)
	_, err = g.call(addr, input, big.NewInt(0))
	g.Require().NotNil(err)

	// Correct.
	input, err = abiObject.Pack("snapshotRound", big.NewInt(2), big.NewInt(2000))
	g.Require().NoError(err)
	_, err = g.call(addr, input, big.NewInt(0))
	g.Require().NoError(err)
}

func (g *GovernanceContractTestSuite) TestConfigurationReading() {
	_, addr := g.newPrefundAccount()

	// CRS.
	input, err := abiObject.Pack("crs", big.NewInt(0))
	g.Require().NoError(err)
	res, err := g.call(addr, input, big.NewInt(0))
	g.Require().NoError(err)
	var crs0 [32]byte
	err = abiObject.Unpack(&crs0, "crs", res)
	g.Require().NoError(err)
	g.Require().Equal(crypto.Keccak256Hash([]byte(g.config.GenesisCRSText)), common.BytesToHash(crs0[:]))

	// Owner.
	input, err = abiObject.Pack("owner")
	g.Require().NoError(err)
	res, err = g.call(addr, input, big.NewInt(0))
	g.Require().NoError(err)
	var owner common.Address
	err = abiObject.Unpack(&owner, "owner", res)
	g.Require().NoError(err)
	g.Require().Equal(g.config.Owner, owner)

	// MinStake.
	input, err = abiObject.Pack("minStake")
	g.Require().NoError(err)
	res, err = g.call(addr, input, big.NewInt(0))
	g.Require().NoError(err)
	var value *big.Int
	err = abiObject.Unpack(&value, "minStake", res)
	g.Require().NoError(err)
	g.Require().Equal(g.config.MinStake.String(), value.String())

	// BlockReward.
	input, err = abiObject.Pack("miningVelocity")
	g.Require().NoError(err)
	res, err = g.call(addr, input, big.NewInt(0))
	g.Require().NoError(err)
	err = abiObject.Unpack(&value, "miningVelocity", res)
	g.Require().NoError(err)
	g.Require().Equal(g.config.MiningVelocity, float32(value.Uint64())/decimalMultiplier)

	// BlockGasLimit.
	input, err = abiObject.Pack("blockGasLimit")
	g.Require().NoError(err)
	res, err = g.call(addr, input, big.NewInt(0))
	g.Require().NoError(err)
	err = abiObject.Unpack(&value, "blockGasLimit", res)
	g.Require().NoError(err)
	g.Require().Equal(g.config.BlockGasLimit, value.Uint64())

	// NumChains.
	input, err = abiObject.Pack("numChains")
	g.Require().NoError(err)
	res, err = g.call(addr, input, big.NewInt(0))
	g.Require().NoError(err)
	err = abiObject.Unpack(&value, "numChains", res)
	g.Require().NoError(err)
	g.Require().Equal(g.config.NumChains, uint32(value.Uint64()))

	// LambdaBA.
	input, err = abiObject.Pack("lambdaBA")
	g.Require().NoError(err)
	res, err = g.call(addr, input, big.NewInt(0))
	g.Require().NoError(err)
	err = abiObject.Unpack(&value, "lambdaBA", res)
	g.Require().NoError(err)
	g.Require().Equal(g.config.LambdaBA, value.Uint64())

	// LambdaDKG.
	input, err = abiObject.Pack("lambdaDKG")
	g.Require().NoError(err)
	res, err = g.call(addr, input, big.NewInt(0))
	g.Require().NoError(err)
	err = abiObject.Unpack(&value, "lambdaDKG", res)
	g.Require().NoError(err)
	g.Require().Equal(g.config.LambdaDKG, value.Uint64())

	// K.
	input, err = abiObject.Pack("k")
	g.Require().NoError(err)
	res, err = g.call(addr, input, big.NewInt(0))
	g.Require().NoError(err)
	err = abiObject.Unpack(&value, "k", res)
	g.Require().NoError(err)
	g.Require().Equal(g.config.K, uint32(value.Uint64()))

	// PhiRatio.
	input, err = abiObject.Pack("phiRatio")
	g.Require().NoError(err)
	res, err = g.call(addr, input, big.NewInt(0))
	g.Require().NoError(err)
	err = abiObject.Unpack(&value, "phiRatio", res)
	g.Require().NoError(err)
	g.Require().Equal(g.config.PhiRatio, float32(value.Uint64())/decimalMultiplier)

	// NotarySetSize.
	input, err = abiObject.Pack("notarySetSize")
	g.Require().NoError(err)
	res, err = g.call(addr, input, big.NewInt(0))
	g.Require().NoError(err)
	err = abiObject.Unpack(&value, "notarySetSize", res)
	g.Require().NoError(err)
	g.Require().Equal(g.config.NotarySetSize, uint32(value.Uint64()))

	// DKGSetSize.
	input, err = abiObject.Pack("dkgSetSize")
	g.Require().NoError(err)
	res, err = g.call(addr, input, big.NewInt(0))
	g.Require().NoError(err)
	err = abiObject.Unpack(&value, "dkgSetSize", res)
	g.Require().NoError(err)
	g.Require().Equal(g.config.DKGSetSize, uint32(value.Uint64()))

	// RoundInterval.
	input, err = abiObject.Pack("roundInterval")
	g.Require().NoError(err)
	res, err = g.call(addr, input, big.NewInt(0))
	g.Require().NoError(err)
	err = abiObject.Unpack(&value, "roundInterval", res)
	g.Require().NoError(err)
	g.Require().Equal(g.config.RoundInterval, value.Uint64())

	// MinBlockInterval.
	input, err = abiObject.Pack("minBlockInterval")
	g.Require().NoError(err)
	res, err = g.call(addr, input, big.NewInt(0))
	g.Require().NoError(err)
	err = abiObject.Unpack(&value, "minBlockInterval", res)
	g.Require().NoError(err)
	g.Require().Equal(g.config.MinBlockInterval, value.Uint64())
}

func (g *GovernanceContractTestSuite) TestReportForkVote() {
	key, addr := g.newPrefundAccount()
	pkBytes := crypto.FromECDSAPub(&key.PublicKey)

	// Stake.
	amount := new(big.Int).Mul(big.NewInt(1e18), big.NewInt(5e5))
	input, err := abiObject.Pack("stake", pkBytes, "Test1", "test1@dexon.org", "Taipei, Taiwan", "https://dexon.org")
	g.Require().NoError(err)
	_, err = g.call(addr, input, amount)
	g.Require().NoError(err)

	pubKey := coreEcdsa.NewPublicKeyFromECDSA(&key.PublicKey)
	privKey := coreEcdsa.NewPrivateKeyFromECDSA(key)
	vote1 := coreTypes.NewVote(coreTypes.VoteCom, coreCommon.NewRandomHash(), uint64(0))
	vote1.ProposerID = coreTypes.NewNodeID(pubKey)

	vote2 := vote1.Clone()
	for vote2.BlockHash == vote1.BlockHash {
		vote2.BlockHash = coreCommon.NewRandomHash()
	}
	vote1.Signature, err = privKey.Sign(coreUtils.HashVote(vote1))
	g.Require().NoError(err)
	vote2.Signature, err = privKey.Sign(coreUtils.HashVote(vote2))
	g.Require().NoError(err)

	vote1Bytes, err := rlp.EncodeToBytes(vote1)
	g.Require().NoError(err)
	vote2Bytes, err := rlp.EncodeToBytes(vote2)
	g.Require().NoError(err)

	// Report wrong type (fork block)
	input, err = abiObject.Pack("report", big.NewInt(2), vote1Bytes, vote2Bytes)
	g.Require().NoError(err)
	_, err = g.call(addr, input, big.NewInt(0))
	g.Require().Error(err)

	input, err = abiObject.Pack("report", big.NewInt(1), vote1Bytes, vote2Bytes)
	g.Require().NoError(err)
	_, err = g.call(addr, input, big.NewInt(0))
	g.Require().NoError(err)

	node := g.s.Node(big.NewInt(0))
	g.Require().Equal(node.Fined, g.s.FineValue(big.NewInt(1)))

	// Duplicate report should fail.
	input, err = abiObject.Pack("report", big.NewInt(1), vote1Bytes, vote2Bytes)
	g.Require().NoError(err)
	_, err = g.call(addr, input, big.NewInt(0))
	g.Require().Error(err)

	// Check if finedRecords is set.
	payloads := [][]byte{vote1Bytes, vote2Bytes}
	sort.Sort(sortBytes(payloads))

	hash := Bytes32(crypto.Keccak256Hash(payloads...))
	input, err = abiObject.Pack("finedRecords", hash)
	g.Require().NoError(err)
	res, err := g.call(addr, input, big.NewInt(0))
	g.Require().NoError(err)

	var value bool
	err = abiObject.Unpack(&value, "finedRecords", res)
	g.Require().NoError(err)
	g.Require().True(value)
}

func (g *GovernanceContractTestSuite) TestReportForkBlock() {
	key, addr := g.newPrefundAccount()
	pkBytes := crypto.FromECDSAPub(&key.PublicKey)

	// Stake.
	amount := new(big.Int).Mul(big.NewInt(1e18), big.NewInt(5e5))
	input, err := abiObject.Pack("stake", pkBytes, "Test1", "test1@dexon.org", "Taipei, Taiwan", "https://dexon.org")
	g.Require().NoError(err)
	_, err = g.call(addr, input, amount)
	g.Require().NoError(err)

	privKey := coreEcdsa.NewPrivateKeyFromECDSA(key)
	block1 := &coreTypes.Block{
		ProposerID: coreTypes.NewNodeID(privKey.PublicKey()),
		ParentHash: coreCommon.NewRandomHash(),
		Timestamp:  time.Now(),
	}

	block2 := block1.Clone()
	for block2.ParentHash == block1.ParentHash {
		block2.ParentHash = coreCommon.NewRandomHash()
	}

	hashBlock := func(block *coreTypes.Block) coreCommon.Hash {
		block.PayloadHash = coreCrypto.Keccak256Hash(block.Payload)
		var err error
		block.Hash, err = coreUtils.HashBlock(block)
		g.Require().NoError(err)
		return block.Hash
	}

	block1.Signature, err = privKey.Sign(hashBlock(block1))
	g.Require().NoError(err)
	block2.Signature, err = privKey.Sign(hashBlock(block2))
	g.Require().NoError(err)

	block1Bytes, err := rlp.EncodeToBytes(block1)
	g.Require().NoError(err)
	block2Bytes, err := rlp.EncodeToBytes(block2)
	g.Require().NoError(err)

	// Report wrong type (fork vote)
	input, err = abiObject.Pack("report", big.NewInt(1), block1Bytes, block2Bytes)
	g.Require().NoError(err)
	_, err = g.call(addr, input, big.NewInt(0))
	g.Require().Error(err)

	input, err = abiObject.Pack("report", big.NewInt(2), block1Bytes, block2Bytes)
	g.Require().NoError(err)
	_, err = g.call(addr, input, big.NewInt(0))
	g.Require().NoError(err)

	node := g.s.Node(big.NewInt(0))
	g.Require().Equal(node.Fined, g.s.FineValue(big.NewInt(2)))

	// Duplicate report should fail.
	input, err = abiObject.Pack("report", big.NewInt(2), block1Bytes, block2Bytes)
	g.Require().NoError(err)
	_, err = g.call(addr, input, big.NewInt(0))
	g.Require().Error(err)

	// Check if finedRecords is set.
	payloads := [][]byte{block1Bytes, block2Bytes}
	sort.Sort(sortBytes(payloads))

	hash := Bytes32(crypto.Keccak256Hash(payloads...))
	input, err = abiObject.Pack("finedRecords", hash)
	g.Require().NoError(err)
	res, err := g.call(addr, input, big.NewInt(0))
	g.Require().NoError(err)

	var value bool
	err = abiObject.Unpack(&value, "finedRecords", res)
	g.Require().NoError(err)
	g.Require().True(value)
}

func (g *GovernanceContractTestSuite) TestMiscVariableReading() {
	privKey, addr := g.newPrefundAccount()
	pk := crypto.FromECDSAPub(&privKey.PublicKey)

	input, err := abiObject.Pack("totalSupply")
	g.Require().NoError(err)
	res, err := g.call(addr, input, big.NewInt(0))
	g.Require().NoError(err)

	input, err = abiObject.Pack("totalStaked")
	g.Require().NoError(err)
	res, err = g.call(addr, input, big.NewInt(0))
	g.Require().NoError(err)

	// Stake.
	amount := new(big.Int).Mul(big.NewInt(1e18), big.NewInt(5e5))
	input, err = abiObject.Pack("stake", pk, "Test1", "test1@dexon.org", "Taipei, Taiwan", "https://dexon.org")
	g.Require().NoError(err)
	_, err = g.call(addr, input, amount)
	g.Require().NoError(err)

	// 1st delegator delegate to 1st node.
	_, addrDelegator := g.newPrefundAccount()
	amount = new(big.Int).Mul(big.NewInt(1e18), big.NewInt(3e5))
	input, err = abiObject.Pack("delegate", addr)
	g.Require().NoError(err)
	_, err = g.call(addrDelegator, input, amount)
	g.Require().NoError(err)

	// 2st delegator delegate to 1st node.
	_, addrDelegator2 := g.newPrefundAccount()
	input, err = abiObject.Pack("delegate", addr)
	g.Require().NoError(err)
	_, err = g.call(addrDelegator2, input, amount)
	g.Require().NoError(err)

	input, err = abiObject.Pack("nodes", big.NewInt(0))
	g.Require().NoError(err)
	res, err = g.call(addr, input, big.NewInt(0))
	g.Require().NoError(err)

	input, err = abiObject.Pack("nodesLength")
	g.Require().NoError(err)
	res, err = g.call(addr, input, big.NewInt(0))
	g.Require().NoError(err)
	var value *big.Int
	err = abiObject.Unpack(&value, "nodesLength", res)
	g.Require().NoError(err)
	g.Require().Equal(1, int(value.Uint64()))

	input, err = abiObject.Pack("nodesOffsetByAddress", addr)
	g.Require().NoError(err)
	res, err = g.call(addr, input, big.NewInt(0))
	g.Require().NoError(err)
	err = abiObject.Unpack(&value, "nodesOffsetByAddress", res)
	g.Require().NoError(err)
	g.Require().Equal(0, int(value.Uint64()))

	id, err := publicKeyToNodeID(pk)
	g.Require().NoError(err)
	input, err = abiObject.Pack("nodesOffsetByID", id)
	g.Require().NoError(err)
	res, err = g.call(addr, input, big.NewInt(0))
	g.Require().NoError(err)
	err = abiObject.Unpack(&value, "nodesOffsetByID", res)
	g.Require().NoError(err)
	g.Require().Equal(0, int(value.Uint64()))

	input, err = abiObject.Pack("delegators", addr, big.NewInt(0))
	g.Require().NoError(err)
	res, err = g.call(addr, input, big.NewInt(0))
	g.Require().NoError(err)

	input, err = abiObject.Pack("delegatorsLength", addr)
	g.Require().NoError(err)
	res, err = g.call(addr, input, big.NewInt(0))
	g.Require().NoError(err)
	err = abiObject.Unpack(&value, "delegatorsLength", res)
	g.Require().NoError(err)
	g.Require().Equal(3, int(value.Uint64()))

	input, err = abiObject.Pack("delegatorsOffset", addr, addrDelegator2)
	g.Require().NoError(err)
	res, err = g.call(addr, input, big.NewInt(0))
	g.Require().NoError(err)
	err = abiObject.Unpack(&value, "delegatorsOffset", res)
	g.Require().NoError(err)
	g.Require().Equal(2, int(value.Uint64()))

	input, err = abiObject.Pack("fineValues", big.NewInt(0))
	g.Require().NoError(err)
	res, err = g.call(addr, input, big.NewInt(0))
	g.Require().NoError(err)
}

func (g *GovernanceContractTestSuite) TestHalvingCondition() {
	// TotalSupply 2.5B reached
	g.s.MiningHalved()
	g.Require().Equal(new(big.Int).Mul(big.NewInt(1e18), big.NewInt(3.25e9)).String(),
		g.s.NextHalvingSupply().String())
	g.Require().Equal(new(big.Int).Mul(big.NewInt(1e18), big.NewInt(0.75e9)).String(),
		g.s.LastHalvedAmount().String())

	// TotalSupply 3.25B reached
	g.s.MiningHalved()
	g.Require().Equal(new(big.Int).Mul(big.NewInt(1e18), big.NewInt(3.625e9)).String(),
		g.s.NextHalvingSupply().String())
	g.Require().Equal(new(big.Int).Mul(big.NewInt(1e18), big.NewInt(0.375e9)).String(),
		g.s.LastHalvedAmount().String())
}

func TestGovernanceContract(t *testing.T) {
	suite.Run(t, new(GovernanceContractTestSuite))
}
