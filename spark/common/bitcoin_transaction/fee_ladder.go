package bitcointransaction

import (
	"bytes"
	"fmt"

	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/lightsparkdev/spark/common"
)

// SP-4089 refund fee ladder, rung table version 1, as the Breez SDK signs it
// (crates/spark/src/utils/fee_ladder.rs). The client derives each ladder's fees
// itself; the SO checks the bounds every version-1 ladder keeps, not the
// client's stepping arithmetic.
const (
	RefundLadderRungTableVersion = 1
	RefundLadderMaxRungs         = 24
	// refundLadderRungSequence is ENABLE_RBF_NO_LOCKTIME: a rung has no CSV.
	refundLadderRungSequence = wire.MaxTxInSequenceNum - 2
	// p2trDustSats is Bitcoin Core's dust threshold for a P2TR output; the top
	// rung pays everything above it.
	p2trDustSats = 330
)

// ValidateRefundLadder checks a refund fee ladder against the anchorless CPFP
// refund it bumps. Each rung must spend the refund's only output back to the
// same script for less, carry a valid key-path signature for that output, and
// the fees must start at 1 sat/vB over the refund + rung package, rise by at
// least 25% per rung and end with the top rung at the dust limit.
func ValidateRefundLadder(refundTx *wire.MsgTx, rungTableVersion uint32, rungs [][]byte) error {
	if rungTableVersion != RefundLadderRungTableVersion {
		return fmt.Errorf("unsupported refund ladder rung table version %d", rungTableVersion)
	}
	if len(rungs) == 0 || len(rungs) > RefundLadderMaxRungs {
		return fmt.Errorf("refund ladder has %d rungs, want 1 to %d", len(rungs), RefundLadderMaxRungs)
	}
	if len(refundTx.TxOut) != 1 {
		return fmt.Errorf("a laddered refund has exactly 1 output, got %d", len(refundTx.TxOut))
	}
	refundOut := refundTx.TxOut[0]
	refundOutPoint := wire.OutPoint{Hash: refundTx.TxHash(), Index: 0}
	prevFetcher := txscript.NewCannedPrevOutputFetcher(refundOut.PkScript, refundOut.Value)

	var prevFee int64
	for i, raw := range rungs {
		rung, err := common.TxFromRawTxBytes(raw)
		if err != nil {
			return fmt.Errorf("rung %d: %w", i, err)
		}
		if len(rung.TxOut) != 1 {
			return fmt.Errorf("rung %d has %d outputs, want 1", i, len(rung.TxOut))
		}
		fee := refundOut.Value - rung.TxOut[0].Value
		expected := wire.NewMsgTx(3)
		expected.AddTxIn(&wire.TxIn{PreviousOutPoint: refundOutPoint, Sequence: refundLadderRungSequence})
		expected.AddTxOut(&wire.TxOut{Value: rung.TxOut[0].Value, PkScript: refundOut.PkScript})
		if err := common.CompareTransactions(expected, rung); err != nil {
			return fmt.Errorf("rung %d does not match expected construction: %w", i, err)
		}
		if !bytes.Equal(rung.TxOut[0].PkScript, refundOut.PkScript) {
			return fmt.Errorf("rung %d pays a different script", i)
		}

		if i == 0 {
			// 1 sat/vB over the package: the refund and its rung are the same size.
			if floor := 2 * txVsize(rung); fee < floor {
				return fmt.Errorf("rung 0 fee %d is below the package floor %d", fee, floor)
			}
		} else if fee*100 < prevFee*125 {
			return fmt.Errorf("rung %d fee %d is less than 1.25x rung %d fee %d", i, fee, i-1, prevFee)
		}
		prevFee = fee

		engine, err := txscript.NewEngine(refundOut.PkScript, rung, 0, txscript.StandardVerifyFlags,
			nil, txscript.NewTxSigHashes(rung, prevFetcher), refundOut.Value, prevFetcher)
		if err != nil {
			return fmt.Errorf("rung %d: %w", i, err)
		}
		if err := engine.Execute(); err != nil {
			return fmt.Errorf("rung %d signature does not verify: %w", i, err)
		}
	}
	if top := refundOut.Value - p2trDustSats; prevFee != top {
		return fmt.Errorf("top rung fee %d, want %d (refund value less P2TR dust)", prevFee, top)
	}
	return nil
}

// txVsize is the BIP-141 virtual size, rounded up.
func txVsize(tx *wire.MsgTx) int64 {
	weight := tx.SerializeSizeStripped()*3 + tx.SerializeSize()
	return int64((weight + 3) / 4)
}
