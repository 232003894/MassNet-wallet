// Modified for MassNet
// Copyright (c) 2013-2015 The btcsuite developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package txscript

import (
	"encoding/binary"
	"errors"
	"github.com/massnetorg/MassNet-wallet/btcec"
	"github.com/massnetorg/MassNet-wallet/config"
	"github.com/massnetorg/MassNet-wallet/logging"
	"github.com/massnetorg/MassNet-wallet/massutil"
	"github.com/massnetorg/MassNet-wallet/wire"
)

// String implements the Stringer interface by returning the name of
// the enum script class. If the enum is invalid then "Invalid" will be
// returned.
func (t ScriptClass) String() string {
	if int(t) > len(scriptClassToName) || int(t) < 0 {
		return "Invalid"
	}
	return scriptClassToName[t]
}

// isMultiSig returns true if the passed script is a multisig transaction, false
// otherwise.
func isMultiSig(pops []parsedOpcode) bool {
	// The absolute minimum is 1 pubkey:
	// OP_0/OP_1-16 <pubkey> OP_1 OP_CHECKMULTISIG
	l := len(pops)
	if l < 4 {
		return false
	}
	if !isSmallInt(pops[0].opcode) {
		return false
	}
	if !isSmallInt(pops[l-2].opcode) {
		return false
	}
	if pops[l-1].opcode.value != OP_CHECKMULTISIG {
		return false
	}

	// Verify the number of pubkeys specified matches the actual number
	// of pubkeys provided.
	if l-2-1 != asSmallInt(pops[l-2].opcode) {
		return false
	}

	for _, pop := range pops[1 : l-2] {
		// Valid pubkeys are either 33 or 65 bytes.
		if len(pop.data) != 33 && len(pop.data) != 65 {
			return false
		}
	}
	return true
}

// isNullData returns true if the passed script is a null data transaction,
// false otherwise.
func isNullData(pops []parsedOpcode) bool {
	// A nulldata transaction is either a single OP_RETURN or an
	// OP_RETURN SMALLDATA (where SMALLDATA is a data push up to
	// MaxDataCarrierSize bytes).
	l := len(pops)
	if l == 1 && pops[0].opcode.value == OP_RETURN {
		return true
	}

	return l == 2 &&
		pops[0].opcode.value == OP_RETURN &&
		pops[1].opcode.value <= OP_PUSHDATA4 &&
		len(pops[1].data) <= MaxDataCarrierSize
}

// scriptType returns the type of the script being inspected from the known
// standard types.
func typeOfScript(pops []parsedOpcode) ScriptClass {
	if isMultiSig(pops) {
		return MultiSigTy
	} else if isNullData(pops) {
		return NullDataTy
	} else if isWitnessScriptHash(pops) {
		return WitnessV0ScriptHashTy
	} else if isLocktimeScriptHash(pops) {
		return LocktimeScriptHashTy
	}
	return NonStandardTy
}

// GetScriptClass returns the class of the script passed.
//
// NonStandardTy will be returned when the script does not parse.
func GetScriptClass(script []byte) ScriptClass {
	pops, err := parseScript(script)
	if err != nil {
		return NonStandardTy
	}
	return typeOfScript(pops)
}

func GetScriptInfo(script []byte) (ScriptClass, []parsedOpcode) {
	pops, err := parseScript(script)

	if err != nil {
		return NonStandardTy, nil
	}
	return typeOfScript(pops), pops
}

func GetParsedOpcode(pops []parsedOpcode, class ScriptClass) (uint64, [20]byte, error) {
	var rsh [20]byte
	height := make([]byte, 8)
	switch class {
	case LocktimeScriptHashTy:
		height = pops[0].data
		scripthash := pops[4].data
		copy(rsh[:], scripthash[:])
	case WitnessV0ScriptHashTy:
		scripthash := pops[1].data
		copy(rsh[:], scripthash[:])
	default:
		logging.CPrint(logging.ERROR, "invalid script hash type", logging.LogFormat{"class": class})
		return 0, [20]byte{}, errors.New("invalid script hash type")
	}
	hgt := binary.LittleEndian.Uint64(height)
	return hgt, rsh, nil
}

// expectedInputs returns the number of arguments required by a script.
// If the script is of unknown type such that the number can not be determined
// then -1 is returned. We are an internal function and thus assume that class
// is the real class of pops (and we can thus assume things that were determined
// while finding out the type).
func expectedInputs(pops []parsedOpcode, class ScriptClass) int {
	switch class {
	case WitnessV0ScriptHashTy:
		// Not including script.  That is handled by the caller.
		return 1
	case LocktimeScriptHashTy:
		return 1
	case MultiSigTy:
		// Standard multisig has a push a small number for the number
		// of sigs and number of keys.  Check the first push instruction
		// to see how many arguments are expected. typeOfScript already
		// checked this so we know it'll be a small int.  Also, due to
		// the original bitcoind bug where OP_CHECKMULTISIG pops an
		// additional item from the stack, add an extra expected input
		// for the extra push that is required to compensate.
		return asSmallInt(pops[0].opcode)

	case NullDataTy:
		fallthrough
	default:
		return -1
	}
}

// ScriptInfo houses information about a script pair that is determined by
// CalcScriptInfo.
type ScriptInfo struct {
	// PkScriptClass is the class of the public key script and is equivalent
	// to calling GetScriptClass on it.
	PkScriptClass ScriptClass

	// NumInputs is the number of inputs provided by the public key script.
	NumInputs int

	// ExpectedInputs is the number of outputs required by the signature
	// script and any pay-to-script-hash scripts. The number will be -1 if
	// unknown.
	ExpectedInputs int

	// SigOps is the number of signature operations in the script pair.
	SigOps int
}

// CalcScriptInfo returns a structure providing data about the provided script
// pair.  It will error if the pair is in someway invalid such that they can not
// be analysed, i.e. if they do not parse or the pkScript is not a push-only
// script
func CalcScriptInfo(pkScript []byte, witness wire.TxWitness) (*ScriptInfo, error) {

	//sigPops, err := parseScript(sigScript)
	//if err != nil {
	//	return nil, err
	//}

	pkPops, err := parseScript(pkScript)
	if err != nil {
		return nil, err
	}

	// Push only sigScript makes little sense.
	si := new(ScriptInfo)
	si.PkScriptClass = typeOfScript(pkPops)

	//Can't have a signature script that doesn't just push data.
	//if !isPushOnly(sigPops) {
	//	return nil, ErrStackP2SHNonPushOnly
	//}

	si.ExpectedInputs = expectedInputs(pkPops, si.PkScriptClass)
	// stack.
	witnessScript := witness[len(witness)-1]
	pops, _ := parseScript(witnessScript)
	witnesssig := witness[0]
	popsigs, _ := parseScript(witnesssig)
	switch {
	case si.PkScriptClass == WitnessV0ScriptHashTy:
		// The witness script is the final element of the witness
		shInputs := expectedInputs(pops, typeOfScript(pops))
		if shInputs == -1 {
			si.ExpectedInputs = -1
		} else {
			si.ExpectedInputs += shInputs
		}

		si.SigOps = GetWitnessSigOpCount(pkScript, witness)
		si.NumInputs = len(popsigs) + 1
	case si.PkScriptClass == LocktimeScriptHashTy:
		shInputs := expectedInputs(pops, typeOfScript(pops))
		if shInputs == -1 {
			si.ExpectedInputs = -1
		} else {
			si.ExpectedInputs += shInputs
		}
		si.SigOps = GetLocktimeScriptSigOpCount(pkScript, witness)
		si.NumInputs = len(popsigs) + 1
	default:
		si.SigOps = getSigOpCount(pkPops, true)

		// All entries pushed to stack (or are OP_RESERVED and exec
		// will fail).
		si.NumInputs = len(popsigs) + len(pops)
	}

	return si, nil
}

func CalcMultiSigStats(script []byte) (int, int, error) {
	pops, err := parseScript(script)
	if err != nil {
		return 0, 0, err
	}

	// A multi-signature script is of the pattern:
	//  NUM_SIGS PUBKEY PUBKEY PUBKEY... NUM_PUBKEYS OP_CHECKMULTISIG
	// Therefore the number of signatures is the oldest item on the stack
	// and the number of pubkeys is the 2nd to last.  Also, the absolute
	// minimum for a multi-signature script is 1 pubkey, so at least 4
	// items must be on the stack per:
	//  OP_1 PUBKEY OP_1 OP_CHECKMULTISIG
	if len(pops) < 4 {
		return 0, 0, ErrStackUnderflow
	}

	numSigs := asSmallInt(pops[0].opcode)
	numPubKeys := asSmallInt(pops[len(pops)-2].opcode)
	return numPubKeys, numSigs, nil
}

// payToWitnessPubKeyHashScript creates a new script to pay to a version 0
// script hash witness program. The passed hash is expected to be valid.
func payToWitnessScriptHashScript(scriptHash []byte) ([]byte, error) {
	return NewScriptBuilder().AddOp(OP_0).AddData(scriptHash).Script()
}

// lockTx
func PayToWitnessScriptHashScript(scriptHash []byte) ([]byte, error) {
	return payToWitnessScriptHashScript(scriptHash)
}

// payToLocktimePubKeyHashScript creates a new script to pay to a version 10
// script hash witness locktime program. The passed hash is expected to be valid.
func payToLocktimeScriptHashScript(scriptHash []byte, locktime []byte) ([]byte, error) {
	return NewScriptBuilder().AddData(locktime).AddOp(OP_CHECKSEQUENCEVERIFY).AddOp(OP_DROP).AddOp(OP_HASH160).AddData(scriptHash).AddOp(OP_EQUAL).Script()
}

// PayToAddrScript creates a new script to pay a transaction output to a lockTx
// address.
func PayToLockAddrScript(addr massutil.Address, locktime int64) ([]byte, error) {

	switch addr := addr.(type) {
	case *massutil.AddressWitnessScriptHash:
		if addr == nil {
			return nil, ErrUnsupportedAddress
		}
		if addr.WitnessVersion() == 0 {
			return payToWitnessScriptHashScript(addr.ScriptAddress())
		}
		if addr.WitnessVersion() == 10 {
			buf := make([]byte, 8)
			binary.LittleEndian.PutUint64(buf, uint64(locktime))
			return payToLocktimeScriptHashScript(addr.ScriptAddress(), buf)
		}
	default:
		logging.CPrint(logging.ERROR, "Invalid address or key", logging.LogFormat{"address": addr})
		return nil, ErrUnsupportedAddress
	}
	return nil, ErrUnsupportedAddress
}

// PayToAddrScript creates a new script to pay a transaction output to a the
// specified address.
func PayToAddrScript(addr massutil.Address) ([]byte, error) {

	switch addr := addr.(type) {
	case *massutil.AddressWitnessScriptHash:
		if addr == nil {
			return nil, ErrUnsupportedAddress
		}
		if addr.WitnessVersion() == 0 {
			return payToWitnessScriptHashScript(addr.ScriptAddress())
		}
	default:
		logging.CPrint(logging.ERROR, "Invalid address or key", logging.LogFormat{"address": addr})
		return nil, ErrUnsupportedAddress
	}
	return nil, ErrUnsupportedAddress
}

//// PayToAddrScript creates a new script to pay a transaction output to a the
//// specified address.
//func PayToAddrScript(addr massutil.Address) ([]byte, error) {
//	switch addr := addr.(type) {
//	case *massutil.AddressWitnessScriptHash:
//		if addr == nil {
//			return nil, ErrUnsupportedAddress
//		}
//		return payToWitnessScriptHashScript(addr.ScriptAddress())
//
//		//case *massutil.AddressPubKey:
//		//	if addr == nil {
//		//		return nil, ErrUnsupportedAddress
//		//	}
//		//	return payToPubKeyScript(addr.ScriptAddress())
//	}
//
//	return nil, ErrUnsupportedAddress
//}

// MultiSigScript returns a valid script for a multisignature redemption where
// nrequired of the keys in pubkeys are required to have signed the transaction
// for success.  An ErrBadNumRequired will be returned if nrequired is larger
// than the number of keys provided.
func MultiSigScript(pubkeys []*massutil.AddressPubKey, nrequired int) ([]byte, error) {
	if len(pubkeys) < nrequired {
		return nil, ErrBadNumRequired
	}

	builder := NewScriptBuilder().AddInt64(int64(nrequired))
	for _, key := range pubkeys {
		builder.AddData(key.ScriptAddress())
	}
	builder.AddInt64(int64(len(pubkeys)))
	builder.AddOp(OP_CHECKMULTISIG)

	return builder.Script()
}

// PushedData returns an array of byte slices containing any pushed data found
// in the passed script.  This includes OP_0, but not OP_1 - OP_16.
func PushedData(script []byte) ([][]byte, error) {
	pops, err := parseScript(script)
	if err != nil {
		return nil, err
	}

	var data [][]byte
	for _, pop := range pops {
		if pop.data != nil {
			data = append(data, pop.data)
		} else if pop.opcode.value == OP_0 {
			data = append(data, nil)
		}
	}
	return data, nil
}

// ExtractPkScriptAddrs returns the type of script, addresses and required
// signatures associated with the passed PkScript.  Note that it only works for
// 'standard' transaction script types.  Any data such as public keys which are
// invalid are omitted from the results.
func ExtractPkScriptAddrs(pkScript []byte, chainParams *config.Params) (ScriptClass, []massutil.Address, []*btcec.PublicKey, int, error) {
	var addrs []massutil.Address
	var pks []*btcec.PublicKey
	var requiredSigs int

	// No valid addresses or required signatures if the script doesn't
	// parse.
	pops, err := parseScript(pkScript)
	if err != nil {
		return NonStandardTy, nil, nil, 0, err
	}

	scriptClass := typeOfScript(pops)
	switch scriptClass {
	case WitnessV0ScriptHashTy:
		// A pay-to-witness-script-hash script is of the form:
		//  OP_0 <32-byte hash>
		// Therefore, the script hash is the second item on the stack.
		// Skip the script hash if it's invalid for some reason.
		requiredSigs = 1
		addr, err := massutil.NewAddressWitnessScriptHash(pops[1].data,
			chainParams)
		if err == nil {
			addrs = append(addrs, addr)
		}
	case LocktimeScriptHashTy:
		requiredSigs = 1
		addr, err := massutil.NewAddressLocktimeScriptHash(pops[4].data,
			chainParams)
		if err == nil {
			addrs = append(addrs, addr)
		}
	case MultiSigTy:
		// A multi-signature script is of the form:
		//  <numsigs> <pubkey> <pubkey> <pubkey>... <numpubkeys> OP_CHECKMULTISIG
		// Therefore the number of required signatures is the 1st item
		// on the stack and the number of public keys is the 2nd to last
		// item on the stack.
		requiredSigs = asSmallInt(pops[0].opcode)
		numPubKeys := asSmallInt(pops[len(pops)-2].opcode)

		// Extract the public keys while skipping any that are invalid.
		addrs = make([]massutil.Address, 0, numPubKeys)
		pks = make([]*btcec.PublicKey, 0, numPubKeys)
		for i := 0; i < numPubKeys; i++ {
			addr, err := massutil.NewAddressPubKey(pops[i+1].data,
				chainParams)
			pk := addr.PubKey()

			if err == nil {
				addrs = append(addrs, addr)
				pks = append(pks, pk)
			}
		}

	case NullDataTy:
		// Null data transactions have no addresses or required
		// signatures.

	case NonStandardTy:
		// Don't attempt to extract addresses or required signatures for
		// nonstandard transactions.
	}

	return scriptClass, addrs, pks, requiredSigs, nil
}
