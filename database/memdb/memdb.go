// Copyright (c) 2013-2014 The btcsuite developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package memdb

import (
	"errors"
	"fmt"
	"math"
	"sync"

	"massnet.org/mass-wallet/config"
	"massnet.org/mass-wallet/database"
	"massnet.org/mass-wallet/logging"
	"massnet.org/mass-wallet/wire"

	"massnet.org/mass-wallet/massutil"
)

// Errors that the various database functions may return.
var (
	ErrDbClosed = errors.New("database is closed")
)

var (
	zeroHash = wire.Hash{}
)

// tTxInsertData holds information about the location and spent status of
// a transaction.
type tTxInsertData struct {
	blockHeight int32
	offset      int
	spentBuf    []bool
}

/*
// newShaHashFromStr converts the passed big-endian hex string into a
// wire.Hash.  It only differs from the one available in wire in that it
// ignores the error since it will only (and must only) be called with
// hard-coded, and therefore known good, hashes.
func newShaHashFromStr(hexStr string) *wire.Hash {
	sha, _ := wire.NewHashFromStr(hexStr)
	return sha
}
*/

// isCoinbaseInput returns whether or not the passed transaction input is a
// coinbase input.  A coinbase is a special transaction created by miners that
// has no inputs.  This is represented in the block chain by a transaction with
// a single input that has a previous output transaction index set to the
// maximum value along with a zero hash.
func isCoinbaseInput(txIn *wire.TxIn) bool {
	prevOut := &txIn.PreviousOutPoint
	if prevOut.Index == math.MaxUint32 && prevOut.Hash.IsEqual(&zeroHash) {
		return true
	}

	return false
}

// isFullySpent returns whether or not a transaction represented by the passed
// transaction insert data is fully spent.  A fully spent transaction is one
// where all outputs are spent.
func isFullySpent(txD *tTxInsertData) bool {
	for _, spent := range txD.spentBuf {
		if !spent {
			return false
		}
	}

	return true
}

// MemDb is a concrete implementation of the database.Db interface which provides
// a memory-only database.  Since it is memory-only, it is obviously not
// persistent and is mostly only useful for testing purposes.
type MemDb struct {
	sync.Mutex
	blocks      []*wire.MsgBlock
	blocksBySha map[wire.Hash]int32
	txns        map[wire.Hash][]*tTxInsertData
	closed      bool
}

// removeTx removes the passed transaction including unspending it.
func (db *MemDb) removeTx(msgTx *wire.MsgTx, txHash *wire.Hash) {
	// Undo all of the spends for the transaction.
	for _, txIn := range msgTx.TxIn {
		if isCoinbaseInput(txIn) {
			continue
		}
		if msgTx.IsCoinBaseTx() {
			continue
		}

		prevOut := &txIn.PreviousOutPoint
		originTxns, exists := db.txns[prevOut.Hash]
		if !exists {
			logging.CPrint(logging.WARN, "unable to find input transaction to unspend index",
				logging.LogFormat{"preOut.Hash": prevOut.Hash, "txHash": txHash, "prevOut.Index": prevOut.Index})
			continue
		}

		originTxD := originTxns[len(originTxns)-1]
		originTxD.spentBuf[prevOut.Index] = false
	}

	// Remove the info for the most recent version of the transaction.
	txns := db.txns[*txHash]
	lastIndex := len(txns) - 1
	txns[lastIndex] = nil
	txns = txns[:lastIndex]
	db.txns[*txHash] = txns

	// Remove the info entry from the map altogether if there not any older
	// versions of the transaction.
	if len(txns) == 0 {
		delete(db.txns, *txHash)
	}

}

// Close cleanly shuts down database.  This is part of the database.Db interface
// implementation.
//
// All data is purged upon close with this implementation since it is a
// memory-only database.
func (db *MemDb) Close() error {
	db.Lock()
	defer db.Unlock()

	if db.closed {
		return ErrDbClosed
	}

	db.blocks = nil
	db.blocksBySha = nil
	db.txns = nil
	db.closed = true
	return nil
}

// DropAfterBlockBySha removes any blocks from the database after the given
// block.  This is different than a simple truncate since the spend information
// for each block must also be unwound.  This is part of the database.Db interface
// implementation.
func (db *MemDb) DropAfterBlockBySha(sha *wire.Hash) error {
	db.Lock()
	defer db.Unlock()

	if db.closed {
		return ErrDbClosed
	}

	// Begin by attempting to find the height associated with the passed
	// hash.
	height, exists := db.blocksBySha[*sha]
	if !exists {
		return fmt.Errorf("block %v does not exist in the database",
			sha)
	}

	// The spend information has to be undone in reverse order, so loop
	// backwards from the last block through the block just after the passed
	// block.  While doing this unspend all transactions in each block and
	// remove the block.
	endHeight := int32(len(db.blocks) - 1)
	for i := endHeight; i > height; i-- {
		// Unspend and remove each transaction in reverse order because
		// later transactions in a block can reference earlier ones.
		transactions := db.blocks[i].Transactions
		for j := len(transactions) - 1; j >= 0; j-- {
			tx := transactions[j]
			txHash := tx.TxHash()
			db.removeTx(tx, &txHash)
		}

		db.blocks[i] = nil
		db.blocks = db.blocks[:i]
	}

	return nil
}

// ExistsSha returns whether or not the given block hash is present in the
// database.  This is part of the database.Db interface implementation.
func (db *MemDb) ExistsSha(sha *wire.Hash) (bool, error) {
	db.Lock()
	defer db.Unlock()

	if db.closed {
		return false, ErrDbClosed
	}

	if _, exists := db.blocksBySha[*sha]; exists {
		return true, nil
	}

	return false, nil
}

// FetchBlockBySha returns a massutil.Block.  The implementation may cache the
// underlying data if desired.  This is part of the database.Db interface
// implementation.
//
// This implementation does not use any additional cache since the entire
// database is already in memory.
func (db *MemDb) FetchBlockBySha(sha *wire.Hash) (*massutil.Block, error) {
	db.Lock()
	defer db.Unlock()

	if db.closed {
		return nil, ErrDbClosed
	}

	if blockHeight, exists := db.blocksBySha[*sha]; exists {
		block := massutil.NewBlock(db.blocks[int(blockHeight)])
		block.SetHeight(blockHeight)
		return block, nil
	}

	return nil, fmt.Errorf("block %v is not in database", sha)
}

// FetchBlockHeightBySha returns the block height for the given hash.  This is
// part of the database.Db interface implementation.
func (db *MemDb) FetchBlockHeightBySha(sha *wire.Hash) (int32, error) {
	db.Lock()
	defer db.Unlock()

	if db.closed {
		return 0, ErrDbClosed
	}

	if blockHeight, exists := db.blocksBySha[*sha]; exists {
		return blockHeight, nil
	}

	return 0, fmt.Errorf("block %v is not in database", sha)
}

// FetchBlockHeaderBySha returns a wire.BlockHeader for the given sha.  The
// implementation may cache the underlying data if desired.  This is part of the
// database.Db interface implementation.
//
// This implementation does not use any additional cache since the entire
// database is already in memory.
func (db *MemDb) FetchBlockHeaderBySha(sha *wire.Hash) (*wire.BlockHeader, error) {
	db.Lock()
	defer db.Unlock()

	if db.closed {
		return nil, ErrDbClosed
	}

	if blockHeight, exists := db.blocksBySha[*sha]; exists {
		return &db.blocks[int(blockHeight)].Header, nil
	}

	return nil, fmt.Errorf("block header %v is not in database", sha)
}

// FetchBlockShaByHeight returns a block hash based on its height in the block
// chain.  This is part of the database.Db interface implementation.
func (db *MemDb) FetchBlockShaByHeight(height int32) (*wire.Hash, error) {
	db.Lock()
	defer db.Unlock()

	if db.closed {
		return nil, ErrDbClosed
	}

	numBlocks := int32(len(db.blocks))
	if height < 0 || height > numBlocks-1 {
		return nil, fmt.Errorf("unable to fetch block height %d since "+
			"it is not within the valid range (%d-%d)", height, 0,
			numBlocks-1)
	}

	msgBlock := db.blocks[height]
	blockHash := msgBlock.BlockHash()
	return &blockHash, nil
}

// FetchHeightRange looks up a range of blocks by the start and ending heights.
// Fetch is inclusive of the start height and exclusive of the ending height.
// To fetch all hashes from the start height until no more are present, use the
// special id `AllShas'.  This is part of the database.Db interface implementation.
func (db *MemDb) FetchHeightRange(startHeight, endHeight int32) ([]wire.Hash, error) {
	db.Lock()
	defer db.Unlock()

	if db.closed {
		return nil, ErrDbClosed
	}

	// When the user passes the special AllShas id, adjust the end height
	// accordingly.
	endHeight = int32(len(db.blocks))

	// Ensure requested heights are sane.
	if startHeight < 0 {
		return nil, fmt.Errorf("start height of fetch range must not "+
			"be less than zero - got %d", startHeight)
	}
	if endHeight < startHeight {
		return nil, fmt.Errorf("end height of fetch range must not "+
			"be less than the start height - got start %d, end %d",
			startHeight, endHeight)
	}

	// Fetch as many as are availalbe within the specified range.
	lastBlockIndex := int32(len(db.blocks) - 1)
	hashList := make([]wire.Hash, 0, endHeight-startHeight)
	for i := startHeight; i < endHeight; i++ {
		if i > lastBlockIndex {
			break
		}

		msgBlock := db.blocks[i]
		blockHash := msgBlock.BlockHash()
		hashList = append(hashList, blockHash)
	}

	return hashList, nil
}

// ExistsTxSha returns whether or not the given transaction hash is present in
// the database and is not fully spent.  This is part of the database.Db interface
// implementation.
func (db *MemDb) ExistsTxSha(sha *wire.Hash) (bool, error) {
	db.Lock()
	defer db.Unlock()

	if db.closed {
		return false, ErrDbClosed
	}

	if txns, exists := db.txns[*sha]; exists {
		return !isFullySpent(txns[len(txns)-1]), nil
	}

	return false, nil
}

// FetchTxBySha returns some data for the given transaction hash. The
// implementation may cache the underlying data if desired.  This is part of the
// database.Db interface implementation.
//
// This implementation does not use any additional cache since the entire
// database is already in memory.
func (db *MemDb) FetchTxBySha(txHash *wire.Hash) ([]*database.TxListReply, error) {
	db.Lock()
	defer db.Unlock()

	if db.closed {
		return nil, ErrDbClosed
	}

	txns, exists := db.txns[*txHash]
	if !exists {
		logging.CPrint(logging.WARN, "FetchTxBySha: requested hash does not exist", logging.LogFormat{"txHash": txHash})
		return nil, database.ErrTxShaMissing
	}

	txHashCopy := *txHash
	replyList := make([]*database.TxListReply, len(txns))
	for i, txD := range txns {
		msgBlock := db.blocks[txD.blockHeight]
		blockSha := msgBlock.BlockHash()

		spentBuf := make([]bool, len(txD.spentBuf))
		copy(spentBuf, txD.spentBuf)
		reply := database.TxListReply{
			Sha:     &txHashCopy,
			Tx:      msgBlock.Transactions[txD.offset],
			BlkSha:  &blockSha,
			Height:  txD.blockHeight,
			TxSpent: spentBuf,
			Err:     nil,
		}
		replyList[i] = &reply
	}

	return replyList, nil
}

// fetchTxByShaList fetches transactions and information about them given an
// array of transaction hashes.  The result is a slice of of TxListReply objects
// which contain the transaction and information about it such as what block and
// block height it's contained in and which outputs are spent.
//
// The includeSpent flag indicates whether or not information about transactions
// which are fully spent should be returned.  When the flag is not set, the
// corresponding entry in the TxListReply slice for fully spent transactions
// will indicate the transaction does not exist.
//
// This function must be called with the db lock held.
func (db *MemDb) fetchTxByShaList(txShaList []*wire.Hash, includeSpent bool) []*database.TxListReply {
	replyList := make([]*database.TxListReply, 0, len(txShaList))
	for i, hash := range txShaList {
		// Every requested entry needs a response, so start with nothing
		// more than a response with the requested hash marked missing.
		// The reply will be updated below with the appropriate
		// information if the transaction exists.
		reply := database.TxListReply{
			Sha: txShaList[i],
			Err: database.ErrTxShaMissing,
		}
		replyList = append(replyList, &reply)

		if db.closed {
			reply.Err = ErrDbClosed
			continue
		}

		if txns, exists := db.txns[*hash]; exists {
			// A given transaction may have duplicates so long as the
			// previous one is fully spent.  We are only interested
			// in the most recent version of the transaction for
			// this function.  The FetchTxBySha function can be
			// used to get all versions of a transaction.
			txD := txns[len(txns)-1]
			if !includeSpent && isFullySpent(txD) {
				continue
			}

			// Look up the referenced block and get its hash.  Set
			// the reply error appropriately and go to the next
			// requested transaction if anything goes wrong.
			msgBlock := db.blocks[txD.blockHeight]
			blockSha := msgBlock.BlockHash()

			// Make a copy of the spent buf to return so the caller
			// can't accidentally modify it.
			spentBuf := make([]bool, len(txD.spentBuf))
			copy(spentBuf, txD.spentBuf)

			// Populate the reply.
			reply.Tx = msgBlock.Transactions[txD.offset]
			reply.BlkSha = &blockSha
			reply.Height = txD.blockHeight
			reply.TxSpent = spentBuf
			reply.Err = nil
		}
	}

	return replyList
}

// FetchTxByShaList returns a TxListReply given an array of transaction hashes.
// The implementation may cache the underlying data if desired.  This is part of
// the database.Db interface implementation.
//
// This implementation does not use any additional cache since the entire
// database is already in memory.

// FetchTxByShaList returns a TxListReply given an array of transaction
// hashes.  This function differs from FetchUnSpentTxByShaList in that it
// returns the most recent version of fully spent transactions.  Due to the
// increased number of transaction fetches, this function is typically more
// expensive than the unspent counterpart, however the specific performance
// details depend on the concrete implementation.  The implementation may cache
// the underlying data if desired.  This is part of the database.Db interface
// implementation.
//
// To fetch all versions of a specific transaction, call FetchTxBySha.
//
// This implementation does not use any additional cache since the entire
// database is already in memory.
func (db *MemDb) FetchTxByShaList(txShaList []*wire.Hash) []*database.TxListReply {
	db.Lock()
	defer db.Unlock()

	return db.fetchTxByShaList(txShaList, true)
}

// FetchUnSpentTxByShaList returns a TxListReply given an array of transaction
// hashes.  Any transactions which are fully spent will indicate they do not
// exist by setting the Err field to TxShaMissing.  The implementation may cache
// the underlying data if desired.  This is part of the database.Db interface
// implementation.
//
// To obtain results which do contain the most recent version of a fully spent
// transactions, call FetchTxByShaList.  To fetch all versions of a specific
// transaction, call FetchTxBySha.
//
// This implementation does not use any additional cache since the entire
// database is already in memory.
func (db *MemDb) FetchUnSpentTxByShaList(txShaList []*wire.Hash) []*database.TxListReply {
	db.Lock()
	defer db.Unlock()

	return db.fetchTxByShaList(txShaList, false)
}

// InsertBlock inserts raw block and transaction data from a block into the
// database.  The first block inserted into the database will be treated as the
// genesis block.  Every subsequent block insert requires the referenced parent
// block to already exist.  This is part of the database.Db interface
// implementation.
func (db *MemDb) InsertBlock(block *massutil.Block) (int32, error) {
	db.Lock()
	defer db.Unlock()

	if db.closed {
		return 0, ErrDbClosed
	}

	// Reject the insert if the previously reference block does not exist
	// except in the case there are no blocks inserted yet where the first
	// inserted block is assumed to be a genesis block.
	msgBlock := block.MsgBlock()
	if _, exists := db.blocksBySha[msgBlock.Header.Previous]; !exists {
		if len(db.blocks) > 0 {
			return 0, database.ErrPrevShaMissing
		}
	}

	// Build a map of in-flight transactions because some of the inputs in
	// this block could be referencing other transactions earlier in this
	// block which are not yet in the chain.
	txInFlight := map[wire.Hash]int{}
	transactions := block.Transactions()
	for i, tx := range transactions {
		txInFlight[*tx.Hash()] = i
	}

	// Loop through all transactions and inputs to ensure there are no error
	// conditions that would prevent them from be inserted into the db.
	// Although these checks could could be done in the loop below, checking
	// for error conditions up front means the code below doesn't have to
	// deal with rollback on errors.
	newHeight := int32(len(db.blocks))
	for i, tx := range transactions {
		for _, txIn := range tx.MsgTx().TxIn {
			if isCoinbaseInput(txIn) {
				continue
			}
			if isCoinBaseTx(tx.MsgTx()) {
				continue
			}

			// It is acceptable for a transaction input to reference
			// the output of another transaction in this block only
			// if the referenced transaction comes before the
			// current one in this block.
			prevOut := &txIn.PreviousOutPoint
			if inFlightIndex, ok := txInFlight[prevOut.Hash]; ok {
				if i <= inFlightIndex {
					logging.CPrint(logging.WARN, "InsertBlock: requested txHash does not exist in-flight", logging.LogFormat{"txHash": tx.Hash()})
					return 0, database.ErrTxShaMissing
				}
			} else {
				originTxns, exists := db.txns[prevOut.Hash]
				if !exists {
					logging.CPrint(logging.WARN, "InsertBlock: requested hash relay on other dose not exists",
						logging.LogFormat{"prevOut.Hash": prevOut.Hash, "tx.Hash": tx.Hash()})
					return 0, database.ErrTxShaMissing
				}
				originTxD := originTxns[len(originTxns)-1]
				if prevOut.Index > uint32(len(originTxD.spentBuf)) {
					logging.CPrint(logging.WARN, "InsertBlock: requested txHash with index does not exist",
						logging.LogFormat{"txHash": tx.Hash(), "index": prevOut.Index})
					return 0, database.ErrTxShaMissing
				}
			}
		}

		// Prevent duplicate transactions in the same block.
		if inFlightIndex, exists := txInFlight[*tx.Hash()]; exists &&
			inFlightIndex < i {
			logging.CPrint(logging.WARN, "Block contains duplicate transaction", logging.LogFormat{"txHash": tx.Hash()})
			return 0, database.ErrDuplicateSha
		}

		// Prevent duplicate transactions unless the old one is fully
		// spent.
		if txns, exists := db.txns[*tx.Hash()]; exists {
			txD := txns[len(txns)-1]
			if !isFullySpent(txD) {
				logging.CPrint(logging.WARN, "Attempt to insert duplicate transaction", logging.LogFormat{"txHash": tx.Hash()})
				return 0, database.ErrDuplicateSha
			}
		}
	}

	db.blocks = append(db.blocks, msgBlock)
	db.blocksBySha[*block.Hash()] = newHeight

	// Insert information about eacj transaction and spend all of the
	// outputs referenced by the inputs to the transactions.
	for i, tx := range block.Transactions() {
		// Insert the transaction data.
		txD := tTxInsertData{
			blockHeight: newHeight,
			offset:      i,
			spentBuf:    make([]bool, len(tx.MsgTx().TxOut)),
		}
		db.txns[*tx.Hash()] = append(db.txns[*tx.Hash()], &txD)
		if isCoinBaseTx(tx.MsgTx()) {
			continue
		}

		// Spend all of the inputs.
		for _, txIn := range tx.MsgTx().TxIn {
			// Coinbase transaction has no inputs.
			if isCoinbaseInput(txIn) {
				continue
			}
			if isCoinBaseTx(tx.MsgTx()) {
				continue
			}

			// Already checked for existing and valid ranges above.
			prevOut := &txIn.PreviousOutPoint
			originTxns := db.txns[prevOut.Hash]
			originTxD := originTxns[len(originTxns)-1]
			originTxD.spentBuf[prevOut.Index] = true

		}
	}

	return newHeight, nil
}

func isCoinBaseTx(msgTx *wire.MsgTx) bool {
	prevOut := &msgTx.TxIn[0].PreviousOutPoint
	if prevOut.Index != math.MaxUint32 || !prevOut.Hash.IsEqual(&wire.Hash{}) {
		return false
	}

	return true
}

// NewestSha returns the hash and block height of the most recent (end) block of
// the block chain.  It will return the zero hash, -1 for the block height, and
// no error (nil) if there are not any blocks in the database yet.  This is part
// of the database.Db interface implementation.
func (db *MemDb) NewestSha() (*wire.Hash, int32, error) {
	db.Lock()
	defer db.Unlock()

	if db.closed {
		return nil, 0, ErrDbClosed
	}

	// When the database has not had a genesis block inserted yet, return
	// values specified by interface contract.
	numBlocks := len(db.blocks)
	if numBlocks == 0 {
		return &zeroHash, -1, nil
	}

	blockSha := db.blocks[numBlocks-1].BlockHash()
	return &blockSha, int32(numBlocks - 1), nil
}

// FetchAddrIndexTip isn't currently implemented. This is a part of the
// database.Db interface implementation.
func (db *MemDb) FetchAddrIndexTip() (*wire.Hash, int32, error) {
	return nil, 0, database.ErrNotImplemented
}

// UpdateAddrIndexForBlock isn't currently implemented. This is a part of the
// database.Db interface implementation.
func (db *MemDb) UpdateAddrIndexForBlock(*wire.Hash, int32,
	database.BlockAddrIndex) error {
	return database.ErrNotImplemented
}

// FetchTxsForAddr isn't currently implemented. This is a part of the database.Db
// interface implementation.
func (db *MemDb) FetchTxsForAddr(massutil.Address, int, int, bool) ([]*database.TxListReply, int, error) {
	return nil, 0, database.ErrNotImplemented
}

func (db *MemDb) FetchUtxosForAddrs(addrs []massutil.Address, chainParams *config.Params) ([][]*database.UtxoListReply, error) {
	return nil, database.ErrNotImplemented
}

// DeleteAddrIndex isn't currently implemented. This is a part of the database.Db
// interface implementation.
func (db *MemDb) DeleteAddrIndex() error {
	return database.ErrNotImplemented
}

// RollbackClose discards the recent database changes to the previously saved
// data at last Sync and closes the database.  This is part of the database.Db
// interface implementation.
//
// The database is completely purged on close with this implementation since the
// entire database is only in memory.  As a result, this function behaves no
// differently than Close.
func (db *MemDb) RollbackClose() error {
	// Rollback doesn't apply to a memory database, so just call Close.
	// Close handles the mutex locks.
	return db.Close()
}

// Sync verifies that the database is coherent on disk and no outstanding
// transactions are in flight.  This is part of the database.Db interface
// implementation.
//
// This implementation does not write any data to disk, so this function only
// grabs a lock to ensure it doesn't return until other operations are complete.
func (db *MemDb) Sync() error {
	db.Lock()
	defer db.Unlock()

	if db.closed {
		return ErrDbClosed
	}

	// There is nothing extra to do to sync the memory database.  However,
	// the lock is still grabbed to ensure the function does not return
	// until other operations are complete.
	return nil
}

// address_package related

func (db *MemDb) InsertRootKey(rootKeyEnc []byte, rootPkExStr string, defaultPasswordUsed bool) error {
	return nil
}

func (db *MemDb) FetchRootKey(rootPkExStr string) ([]byte, bool, error) {
	return nil, false, nil
}
func (db *MemDb) FetchAllRootPkStr() ([]string, error) {
	return nil, nil
}

func (db *MemDb) InitChildKeyNum(rootPkExStr string) error {
	return nil
}

func (db *MemDb) FetchChildKeyNum(rootPkExStr string) (int, error) {
	return 0, nil
}

func (db *MemDb) UpdateChildKeyNum(rootPkExStr string) (int, error) {
	return 0, nil
}

func (db *MemDb) InsertWitnessAddr(witAddr string, redeemScript []byte) error {
	return nil
}

func (db *MemDb) FetchWitnessAddrToRedeem() (map[string][]byte, error) {
	return nil, nil
}

func (db *MemDb) ImportChildKeyNum(rootPkExStr string, num string) error {
	return nil
}

// newMemDb returns a new memory-only database ready for block inserts.
func newMemDb() *MemDb {
	db := MemDb{
		blocks:      make([]*wire.MsgBlock, 0, 200000),
		blocksBySha: make(map[wire.Hash]int32),
		txns:        make(map[wire.Hash][]*tTxInsertData),
	}
	return &db
}
