package handler

import (
	"math/rand/v2"
	"testing"

	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/keys"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so/db"
	st "github.com/lightsparkdev/spark/so/ent/schema/schematype"
	"github.com/stretchr/testify/require"
)

// dropAnchor returns the CPFP refund without its ephemeral anchor output.
func dropAnchor(t *testing.T, rawTx []byte) []byte {
	tx, err := common.TxFromRawTxBytes(rawTx)
	require.NoError(t, err)
	tx.TxOut = tx.TxOut[:1]
	out, err := common.SerializeTx(tx)
	require.NoError(t, err)
	return out
}

// topRungLadder is the smallest valid ladder: a single rung paying all but
// the dust limit in fees, key-path signed by signer.
func topRungLadder(t *testing.T, refundRaw []byte, signer keys.Private) *pb.FeeBumpLadder {
	refund, err := common.TxFromRawTxBytes(refundRaw)
	require.NoError(t, err)
	out := refund.TxOut[0]
	rung := wire.NewMsgTx(3)
	rung.AddTxIn(&wire.TxIn{PreviousOutPoint: wire.OutPoint{Hash: refund.TxHash()}, Sequence: wire.MaxTxInSequenceNum - 2})
	rung.AddTxOut(&wire.TxOut{Value: 330, PkScript: out.PkScript})
	fetcher := txscript.NewCannedPrevOutputFetcher(out.PkScript, out.Value)
	sig, err := txscript.RawTxInTaprootSignature(rung, txscript.NewTxSigHashes(rung, fetcher), 0,
		out.Value, out.PkScript, nil, txscript.SigHashDefault, signer.ToBTCEC())
	require.NoError(t, err)
	rung.TxIn[0].Witness = wire.TxWitness{sig}
	signed, err := common.SerializeTx(rung)
	require.NoError(t, err)
	return &pb.FeeBumpLadder{RungTableVersion: 1, Rungs: []*pb.FeeBumpRung{{SignedTx: signed}}}
}

// A claim's CPFP refund drops its anchor only together with a valid fee
// ladder, and a ladder never rides on the anchored refund.
func TestValidateReceivedRefundTransactions_RefundLadder(t *testing.T) {
	tests := []struct {
		name       string
		anchorless bool
		ladder     func(t *testing.T, refund []byte, owner keys.Private, rng *rand.ChaCha8) *pb.FeeBumpLadder
		errText    string
	}{
		{
			name:       "anchorless refund with a valid ladder",
			anchorless: true,
			ladder: func(t *testing.T, refund []byte, owner keys.Private, _ *rand.ChaCha8) *pb.FeeBumpLadder {
				return topRungLadder(t, refund, owner)
			},
		},
		{name: "anchorless refund without a ladder", anchorless: true, errText: "expected 2 outputs, got 1"},
		{
			name: "anchored refund with a ladder",
			ladder: func(t *testing.T, refund []byte, owner keys.Private, _ *rand.ChaCha8) *pb.FeeBumpLadder {
				return topRungLadder(t, dropAnchor(t, refund), owner)
			},
			errText: "expected 1 outputs, got 2",
		},
		{
			name:       "ladder signed by someone else",
			anchorless: true,
			ladder: func(t *testing.T, refund []byte, _ keys.Private, rng *rand.ChaCha8) *pb.FeeBumpLadder {
				return topRungLadder(t, refund, keys.MustGeneratePrivateKeyFromRand(rng))
			},
			errText: "invalid refund fee ladder",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rng := rand.NewChaCha8([32]byte{0xB1})
			ctx, _ := db.NewTestSQLiteContext(t)
			owner := keys.MustGeneratePrivateKeyFromRand(rng)
			leaf := createTestTreeNodeForValidation(t, rng, owner.Public())
			job := createSwapClaimJobForLeaf(t, rng, leaf, owner.Public(), 0)
			if tc.anchorless {
				job.RefundTxSigningJob.RawTx = dropAnchor(t, job.RefundTxSigningJob.RawTx)
			}
			if tc.ladder != nil {
				job.RefundLadder = tc.ladder(t, job.RefundTxSigningJob.RawTx, owner, rng)
			}

			err := validateReceivedRefundTransactions(ctx, job, leaf, st.TransferTypePreimageSwap, nil, nil)
			if tc.errText == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tc.errText)
			}
		})
	}
}
