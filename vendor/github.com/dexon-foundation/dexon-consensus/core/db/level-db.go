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

package db

import (
	"encoding/binary"

	"github.com/syndtr/goleveldb/leveldb"

	"github.com/dexon-foundation/dexon-consensus/common"
	"github.com/dexon-foundation/dexon-consensus/core/crypto/dkg"
	"github.com/dexon-foundation/dexon-consensus/core/types"
	"github.com/dexon-foundation/dexon/rlp"
)

var (
	blockKeyPrefix               = []byte("b-")
	compactionChainTipInfoKey    = []byte("cc-tip")
	dkgPrivateKeyKeyPrefix       = []byte("dkg-prvs")
	dkgMasterPrivateSharesPrefix = []byte("dkg-master-private-shares")
)

type compactionChainTipInfo struct {
	Height uint64      `json:"height"`
	Hash   common.Hash `json:"hash"`
}

// LevelDBBackedDB is a leveldb backed DB implementation.
type LevelDBBackedDB struct {
	db *leveldb.DB
}

// NewLevelDBBackedDB initialize a leveldb-backed database.
func NewLevelDBBackedDB(
	path string) (lvl *LevelDBBackedDB, err error) {

	dbInst, err := leveldb.OpenFile(path, nil)
	if err != nil {
		return
	}
	lvl = &LevelDBBackedDB{db: dbInst}
	return
}

// Close implement Closer interface, which would release allocated resource.
func (lvl *LevelDBBackedDB) Close() error {
	return lvl.db.Close()
}

// HasBlock implements the Reader.Has method.
func (lvl *LevelDBBackedDB) HasBlock(hash common.Hash) bool {
	exists, err := lvl.internalHasBlock(lvl.getBlockKey(hash))
	if err != nil {
		// TODO(missionliao): Modify the interface to return error.
		panic(err)
	}
	return exists
}

func (lvl *LevelDBBackedDB) internalHasBlock(key []byte) (bool, error) {
	return lvl.db.Has(key, nil)
}

// GetBlock implements the Reader.GetBlock method.
func (lvl *LevelDBBackedDB) GetBlock(
	hash common.Hash) (block types.Block, err error) {
	queried, err := lvl.db.Get(lvl.getBlockKey(hash), nil)
	if err != nil {
		if err == leveldb.ErrNotFound {
			err = ErrBlockDoesNotExist
		}
		return
	}
	err = rlp.DecodeBytes(queried, &block)
	return
}

// UpdateBlock implements the Writer.UpdateBlock method.
func (lvl *LevelDBBackedDB) UpdateBlock(block types.Block) (err error) {
	// NOTE: we didn't handle changes of block hash (and it
	//       should not happen).
	marshaled, err := rlp.EncodeToBytes(&block)
	if err != nil {
		return
	}
	blockKey := lvl.getBlockKey(block.Hash)
	exists, err := lvl.internalHasBlock(blockKey)
	if err != nil {
		return
	}
	if !exists {
		err = ErrBlockDoesNotExist
		return
	}
	err = lvl.db.Put(blockKey, marshaled, nil)
	return
}

// PutBlock implements the Writer.PutBlock method.
func (lvl *LevelDBBackedDB) PutBlock(block types.Block) (err error) {
	marshaled, err := rlp.EncodeToBytes(&block)
	if err != nil {
		return
	}
	blockKey := lvl.getBlockKey(block.Hash)
	exists, err := lvl.internalHasBlock(blockKey)
	if err != nil {
		return
	}
	if exists {
		err = ErrBlockExists
		return
	}
	err = lvl.db.Put(blockKey, marshaled, nil)
	return
}

// GetAllBlocks implements Reader.GetAllBlocks method, which allows callers
// to retrieve all blocks in DB.
func (lvl *LevelDBBackedDB) GetAllBlocks() (BlockIterator, error) {
	// TODO (mission): Implement this part via goleveldb's iterator.
	return nil, ErrNotImplemented
}

// PutCompactionChainTipInfo saves tip of compaction chain into the database.
func (lvl *LevelDBBackedDB) PutCompactionChainTipInfo(
	blockHash common.Hash, height uint64) error {
	marshaled, err := rlp.EncodeToBytes(&compactionChainTipInfo{
		Hash:   blockHash,
		Height: height,
	})
	if err != nil {
		return err
	}
	// Check current cached tip info to make sure the one to be updated is
	// valid.
	info, err := lvl.internalGetCompactionChainTipInfo()
	if err != nil {
		return err
	}
	if info.Height+1 != height {
		return ErrInvalidCompactionChainTipHeight
	}
	return lvl.db.Put(compactionChainTipInfoKey, marshaled, nil)
}

func (lvl *LevelDBBackedDB) internalGetCompactionChainTipInfo() (
	info compactionChainTipInfo, err error) {
	queried, err := lvl.db.Get(compactionChainTipInfoKey, nil)
	if err != nil {
		if err == leveldb.ErrNotFound {
			err = nil
		}
		return
	}
	err = rlp.DecodeBytes(queried, &info)
	return
}

// GetCompactionChainTipInfo get the tip info of compaction chain into the
// database.
func (lvl *LevelDBBackedDB) GetCompactionChainTipInfo() (
	hash common.Hash, height uint64) {
	info, err := lvl.internalGetCompactionChainTipInfo()
	if err != nil {
		panic(err)
	}
	hash, height = info.Hash, info.Height
	return
}

// HasDKGPrivateKey check existence of DKG private key of one round.
func (lvl *LevelDBBackedDB) HasDKGPrivateKey(round uint64) (bool, error) {
	return lvl.db.Has(lvl.getDKGPrivateKeyKey(round), nil)
}

// HasDKGMasterPrivateSharesKey check existence of DKG master private shares of one round.
func (lvl *LevelDBBackedDB) HasDKGMasterPrivateSharesKey(round uint64) (bool, error) {
	return lvl.db.Has(lvl.getDKGMasterPrivateSharesKey(round), nil)
}

// GetDKGPrivateKey get DKG private key of one round.
func (lvl *LevelDBBackedDB) GetDKGPrivateKey(round uint64) (
	prv dkg.PrivateKey, err error) {
	queried, err := lvl.db.Get(lvl.getDKGPrivateKeyKey(round), nil)
	if err != nil {
		if err == leveldb.ErrNotFound {
			err = ErrDKGPrivateKeyDoesNotExist
		}
		return
	}
	err = rlp.DecodeBytes(queried, &prv)
	return
}

// PutDKGPrivateKey save DKG private key of one round.
func (lvl *LevelDBBackedDB) PutDKGPrivateKey(
	round uint64, prv dkg.PrivateKey) error {
	// Check existence.
	exists, err := lvl.HasDKGPrivateKey(round)
	if err != nil {
		return err
	}
	if exists {
		return ErrDKGPrivateKeyExists
	}
	marshaled, err := rlp.EncodeToBytes(&prv)
	if err != nil {
		return err
	}
	return lvl.db.Put(
		lvl.getDKGPrivateKeyKey(round), marshaled, nil)
}

// GetDKGMasterPrivateShares get DKG master private shares of one round.
func (lvl *LevelDBBackedDB) GetDKGMasterPrivateShares(round uint64) (
	shares dkg.PrivateKeyShares, err error) {
	queried, err := lvl.db.Get(lvl.getDKGMasterPrivateSharesKey(round), nil)
	if err != nil {
		if err == leveldb.ErrNotFound {
			err = ErrDKGMasterPrivateSharesDoesNotExist
		}
		return
	}

	err = rlp.DecodeBytes(queried, &shares)
	return
}

// PutOrUpdateDKGMasterPrivateShares save DKG master private shares of one round.
func (lvl *LevelDBBackedDB) PutOrUpdateDKGMasterPrivateShares(
	round uint64, shares dkg.PrivateKeyShares) error {
	marshaled, err := rlp.EncodeToBytes(&shares)
	if err != nil {
		return err
	}
	return lvl.db.Put(
		lvl.getDKGMasterPrivateSharesKey(round), marshaled, nil)
}

func (lvl *LevelDBBackedDB) getBlockKey(hash common.Hash) (ret []byte) {
	ret = make([]byte, len(blockKeyPrefix)+len(hash[:]))
	copy(ret, blockKeyPrefix)
	copy(ret[len(blockKeyPrefix):], hash[:])
	return
}

func (lvl *LevelDBBackedDB) getDKGPrivateKeyKey(
	round uint64) (ret []byte) {
	ret = make([]byte, len(dkgPrivateKeyKeyPrefix)+8)
	copy(ret, dkgPrivateKeyKeyPrefix)
	binary.LittleEndian.PutUint64(
		ret[len(dkgPrivateKeyKeyPrefix):], round)
	return
}

func (lvl *LevelDBBackedDB) getDKGMasterPrivateSharesKey(round uint64) (ret []byte) {
	ret = make([]byte, len(dkgMasterPrivateSharesPrefix)+8)
	copy(ret, dkgMasterPrivateSharesPrefix)
	binary.LittleEndian.PutUint64(ret[len(dkgMasterPrivateSharesPrefix):], round)
	return
}
