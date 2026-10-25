package bitcointransaction_test

import (
	"bytes"
	"testing"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	bitcointransaction "github.com/lightsparkdev/spark/common/bitcoin_transaction"
	"github.com/stretchr/testify/require"
)

const ladderRefundValue = 10_000

type ladderFixture struct {
	key      *btcec.PrivateKey
	refundTx *wire.MsgTx
	script   []byte
}

func newLadderFixture(t *testing.T) ladderFixture {
	key, err := btcec.NewPrivateKey()
	require.NoError(t, err)
	script, err := txscript.PayToTaprootScript(txscript.ComputeTaprootKeyNoScript(key.PubKey()))
	require.NoError(t, err)
	refund := wire.NewMsgTx(3)
	refund.AddTxIn(&wire.TxIn{PreviousOutPoint: wire.OutPoint{Hash: chainhash.Hash{1}}, Sequence: 1<<30 | 1900})
	refund.AddTxOut(&wire.TxOut{Value: ladderRefundValue, PkScript: script})
	return ladderFixture{key: key, refundTx: refund, script: script}
}

// clientLadderFees follows the client's version-1 rules: start at 1 sat/vB over
// the 222 vB package, step by 1.25x, top rung at the dust limit.
func clientLadderFees(value int64) []int64 {
	top := value - 330
	fees := []int64{222}
	for len(fees) < 23 {
		next := (fees[len(fees)-1]*125 + 99) / 100
		if next > top*100/125 {
			break
		}
		fees = append(fees, next)
	}
	return append(fees, top)
}

func (f ladderFixture) rung(t *testing.T, fee int64, key *btcec.PrivateKey) *wire.MsgTx {
	rung := wire.NewMsgTx(3)
	rung.AddTxIn(&wire.TxIn{PreviousOutPoint: wire.OutPoint{Hash: f.refundTx.TxHash()}, Sequence: wire.MaxTxInSequenceNum - 2})
	rung.AddTxOut(&wire.TxOut{Value: ladderRefundValue - fee, PkScript: f.script})
	f.sign(t, rung, key)
	return rung
}

func (f ladderFixture) sign(t *testing.T, rung *wire.MsgTx, key *btcec.PrivateKey) {
	fetcher := txscript.NewCannedPrevOutputFetcher(f.script, ladderRefundValue)
	sig, err := txscript.RawTxInTaprootSignature(rung, txscript.NewTxSigHashes(rung, fetcher), 0,
		ladderRefundValue, f.script, nil, txscript.SigHashDefault, key)
	require.NoError(t, err)
	rung.TxIn[0].Witness = wire.TxWitness{sig}
}

func serialize(t *testing.T, txs ...*wire.MsgTx) [][]byte {
	out := make([][]byte, len(txs))
	for i, tx := range txs {
		var buf bytes.Buffer
		require.NoError(t, tx.Serialize(&buf))
		out[i] = buf.Bytes()
	}
	return out
}

func (f ladderFixture) ladder(t *testing.T, fees []int64) []*wire.MsgTx {
	rungs := make([]*wire.MsgTx, len(fees))
	for i, fee := range fees {
		rungs[i] = f.rung(t, fee, f.key)
	}
	return rungs
}

func TestValidateRefundLadder_AcceptsAClientLadder(t *testing.T) {
	f := newLadderFixture(t)
	fees := clientLadderFees(ladderRefundValue)
	require.Greater(t, len(fees), 2)
	require.NoError(t, bitcointransaction.ValidateRefundLadder(f.refundTx, 1, serialize(t, f.ladder(t, fees)...)))
}

func TestValidateRefundLadder_AcceptsASingleTopRung(t *testing.T) {
	f := newLadderFixture(t)
	require.NoError(t, bitcointransaction.ValidateRefundLadder(f.refundTx, 1, serialize(t, f.rung(t, ladderRefundValue-330, f.key))))
}

func TestValidateRefundLadder_Rejects(t *testing.T) {
	fees := clientLadderFees(ladderRefundValue)
	tests := []struct {
		name    string
		version uint32
		build   func(t *testing.T, f ladderFixture) []*wire.MsgTx
		refund  func(f ladderFixture) *wire.MsgTx
		errText string
	}{
		{name: "unknown rung table version", version: 2, errText: "rung table version"},
		{name: "no rungs", build: func(*testing.T, ladderFixture) []*wire.MsgTx { return nil }, errText: "0 rungs"},
		{
			name: "more than 24 rungs",
			build: func(t *testing.T, f ladderFixture) []*wire.MsgTx {
				rungs := f.ladder(t, fees)
				for len(rungs) <= 24 {
					rungs = append(rungs, rungs[0])
				}
				return rungs
			},
			errText: "want 1 to 24",
		},
		{
			name: "anchored refund",
			refund: func(f ladderFixture) *wire.MsgTx {
				anchored := f.refundTx.Copy()
				anchored.AddTxOut(&wire.TxOut{Value: 0, PkScript: []byte{txscript.OP_TRUE, 0x02, 0x4e, 0x73}})
				return anchored
			},
			errText: "exactly 1 output",
		},
		{
			name: "rung spends another output",
			build: func(t *testing.T, f ladderFixture) []*wire.MsgTx {
				rungs := f.ladder(t, fees)
				rungs[1].TxIn[0].PreviousOutPoint.Index = 1
				return rungs
			},
			errText: "rung 1 does not match",
		},
		{
			name: "rung with a timelock",
			build: func(t *testing.T, f ladderFixture) []*wire.MsgTx {
				rungs := f.ladder(t, fees)
				rungs[0].TxIn[0].Sequence = 10
				f.sign(t, rungs[0], f.key)
				return rungs
			},
			errText: "rung 0 does not match",
		},
		{
			name: "rung paying another script",
			build: func(t *testing.T, f ladderFixture) []*wire.MsgTx {
				rungs := f.ladder(t, fees)
				rungs[2].TxOut[0].PkScript = append([]byte{}, f.script...)
				rungs[2].TxOut[0].PkScript[5] ^= 1
				return rungs
			},
			errText: "rung 2 does not match",
		},
		{
			name: "first rung under 1 sat/vB",
			build: func(t *testing.T, f ladderFixture) []*wire.MsgTx {
				return f.ladder(t, append([]int64{221}, fees[1:]...))
			},
			errText: "below the package floor",
		},
		{
			name: "step under 1.25x",
			build: func(t *testing.T, f ladderFixture) []*wire.MsgTx {
				return f.ladder(t, append([]int64{fees[0], fees[0]*125/100 - 1}, fees[2:]...))
			},
			errText: "less than 1.25x",
		},
		{
			name: "top rung short of the dust limit",
			build: func(t *testing.T, f ladderFixture) []*wire.MsgTx {
				short := append([]int64{}, fees...)
				short[len(short)-1]--
				return f.ladder(t, short)
			},
			errText: "top rung fee",
		},
		{
			name: "rung signed by another key",
			build: func(t *testing.T, f ladderFixture) []*wire.MsgTx {
				other, err := btcec.NewPrivateKey()
				require.NoError(t, err)
				rungs := f.ladder(t, fees)
				f.sign(t, rungs[3], other)
				return rungs
			},
			errText: "rung 3 signature does not verify",
		},
		{
			name: "unsigned rung",
			build: func(t *testing.T, f ladderFixture) []*wire.MsgTx {
				rungs := f.ladder(t, fees)
				rungs[0].TxIn[0].Witness = nil
				return rungs
			},
			errText: "rung 0 signature does not verify",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newLadderFixture(t)
			version := tc.version
			if version == 0 {
				version = 1
			}
			rungs := f.ladder(t, fees)
			if tc.build != nil {
				rungs = tc.build(t, f)
			}
			refund := f.refundTx
			if tc.refund != nil {
				refund = tc.refund(f)
			}
			err := bitcointransaction.ValidateRefundLadder(refund, version, serialize(t, rungs...))
			require.ErrorContains(t, err, tc.errText)
		})
	}
}
