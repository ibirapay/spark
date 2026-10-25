package watchtower

import (
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so/ent"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestPickRefundRung(t *testing.T) {
	rates := []int64{1_000, 1_250, 1_600, 2_000, 2_500} // sat/kvB
	tests := []struct {
		name        string
		target      int64
		blocksSince uint64
		want        int
	}{
		{name: "no estimate starts at the cheapest", target: 0, want: 0},
		{name: "cheapest rung meeting the estimate", target: 1_500, want: 2},
		{name: "estimate above every rung takes the top", target: 9_000, want: 4},
		{name: "climbs with the defense window", target: 0, blocksSince: 50, want: 2},
		{name: "never below the estimate", target: 2_000, blocksSince: 25, want: 3},
		{name: "top rung by the end of the window", target: 0, blocksSince: 100, want: 4},
		{name: "stays on top past the window", target: 0, blocksSince: 500, want: 4},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, pickRefundRung(rates, tc.target, tc.blocksSince, 100))
		})
	}
}

type ladderNode struct {
	node  *ent.TreeNode
	rungs [][]byte
}

// newLadderNode is a leaf whose node tx confirmed at height 100, with a signed
// anchorless refund timelocked 1900 blocks and a three-rung ladder for it.
func newLadderNode(t *testing.T) ladderNode {
	p2trScript := append([]byte{0x51, 0x20}, make([]byte, 32)...)
	refund := wire.NewMsgTx(3)
	refund.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{Hash: chainhash.Hash{7}},
		Sequence:         1<<30 | 1900,
		Witness:          wire.TxWitness{make([]byte, 64)},
	})
	refund.AddTxOut(&wire.TxOut{Value: 10_000, PkScript: p2trScript})
	refundRaw, err := common.SerializeTx(refund)
	require.NoError(t, err)

	ladder := &pb.FeeBumpLadder{RungTableVersion: 1}
	var rungs [][]byte
	for _, fee := range []int64{222, 278, 9_670} {
		rung := wire.NewMsgTx(3)
		rung.AddTxIn(&wire.TxIn{
			PreviousOutPoint: wire.OutPoint{Hash: refund.TxHash()},
			Sequence:         wire.MaxTxInSequenceNum - 2,
			Witness:          wire.TxWitness{make([]byte, 64)},
		})
		rung.AddTxOut(&wire.TxOut{Value: 10_000 - fee, PkScript: p2trScript})
		raw, err := common.SerializeTx(rung)
		require.NoError(t, err)
		rungs = append(rungs, raw)
		ladder.Rungs = append(ladder.Rungs, &pb.FeeBumpRung{SignedTx: raw})
	}
	ladderRaw, err := proto.Marshal(ladder)
	require.NoError(t, err)
	return ladderNode{
		node:  &ent.TreeNode{RawRefundTx: refundRaw, RawRefundLadder: ladderRaw, NodeConfirmationHeight: 100},
		rungs: rungs,
	}
}

func submittedPackage(t *testing.T, client *mockBitcoinClient) []string {
	for _, req := range client.rawRequests {
		if req.method == "submitpackage" {
			var txs []string
			require.NoError(t, json.Unmarshal(req.params[0], &txs))
			return txs
		}
	}
	return nil
}

func TestTryBroadcastRefundLadder(t *testing.T) {
	ctx := t.Context()
	success := json.RawMessage(`{"package_msg":"success"}`)

	t.Run("timelocked refund is left alone", func(t *testing.T) {
		n := newLadderNode(t)
		client := &mockBitcoinClient{rawResponses: map[string]json.RawMessage{"submitpackage": success}}
		sent, err := tryBroadcastRefundLadder(ctx, client, n.node, 1999, btcnetwork.Regtest)
		require.NoError(t, err)
		require.False(t, sent)
		require.Empty(t, client.rawRequests)
	})

	t.Run("expired refund goes out with the rung meeting the estimate", func(t *testing.T) {
		n := newLadderNode(t)
		client := &mockBitcoinClient{rawResponses: map[string]json.RawMessage{
			// 0.00001200 BTC/kvB = 1200 sat/kvB: rung 0 pays 222/222 vB = 1000, rung 1 pays 1252.
			"estimatesmartfee": json.RawMessage(`{"feerate":0.000012,"blocks":1}`),
			"submitpackage":    success,
		}}
		sent, err := tryBroadcastRefundLadder(ctx, client, n.node, 2000, btcnetwork.Regtest)
		require.NoError(t, err)
		require.True(t, sent)
		require.Equal(t, []string{hex.EncodeToString(n.node.RawRefundTx), hex.EncodeToString(n.rungs[1])}, submittedPackage(t, client))
	})

	t.Run("no estimate falls back to the cheapest rung", func(t *testing.T) {
		n := newLadderNode(t)
		client := &mockBitcoinClient{rawResponses: map[string]json.RawMessage{
			"estimatesmartfee": json.RawMessage(`{"errors":["Insufficient data or no feerate found"],"blocks":0}`),
			"submitpackage":    success,
		}}
		sent, err := tryBroadcastRefundLadder(ctx, client, n.node, 2000, btcnetwork.Regtest)
		require.NoError(t, err)
		require.True(t, sent)
		require.Equal(t, hex.EncodeToString(n.rungs[0]), submittedPackage(t, client)[1])
	})

	t.Run("a ladder for an earlier refund is ignored", func(t *testing.T) {
		n := newLadderNode(t)
		refund, err := common.TxFromRawTxBytes(n.node.RawRefundTx)
		require.NoError(t, err)
		refund.TxIn[0].PreviousOutPoint.Index = 1
		n.node.RawRefundTx, err = common.SerializeTx(refund)
		require.NoError(t, err)
		client := &mockBitcoinClient{rawResponses: map[string]json.RawMessage{"submitpackage": success}}
		sent, err := tryBroadcastRefundLadder(ctx, client, n.node, 2000, btcnetwork.Regtest)
		require.NoError(t, err)
		require.False(t, sent)
		require.Nil(t, submittedPackage(t, client))
	})

	t.Run("a rejected package is an error", func(t *testing.T) {
		n := newLadderNode(t)
		client := &mockBitcoinClient{rawResponses: map[string]json.RawMessage{
			"submitpackage": json.RawMessage(`{"package_msg":"transaction failed"}`),
		}}
		sent, err := tryBroadcastRefundLadder(ctx, client, n.node, 2000, btcnetwork.Regtest)
		require.ErrorContains(t, err, "package rejected")
		require.False(t, sent)
	})
}
