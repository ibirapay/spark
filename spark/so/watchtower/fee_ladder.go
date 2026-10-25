package watchtower

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"

	"github.com/btcsuite/btcd/wire"
	"github.com/lightsparkdev/spark"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/logging"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/lightsparkdev/spark/so/ent"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"google.golang.org/protobuf/proto"
)

// refundLadderDefenseWindow is how many blocks the current owner's CPFP refund
// is ahead of the previous owner's: each transfer lowers the refund timelock by
// one interval. The watchtower spends that head start climbing the ladder, so
// the top rung goes out before a stale refund can.
const refundLadderDefenseWindow = spark.TimeLockInterval

// tryBroadcastRefundLadder submits the node's signed CPFP refund together with
// one rung of its fee ladder as a package, once the refund's timelock has
// expired. It reports whether a package went out; a node with no ladder for
// its current refund, or one still timelocked, is not an error.
func tryBroadcastRefundLadder(ctx context.Context, btcClient bitcoinClient, node *ent.TreeNode, blockHeight int64, network btcnetwork.Network) (bool, error) {
	if len(node.RawRefundLadder) == 0 || len(node.RawRefundTx) == 0 {
		return false, nil
	}
	refundTx, err := common.TxFromRawTxBytes(node.RawRefundTx)
	if err != nil {
		return false, fmt.Errorf("parse refund tx: %w", err)
	}
	if len(refundTx.TxIn) != 1 || len(refundTx.TxOut) != 1 || len(refundTx.TxIn[0].Witness) == 0 {
		// Not a signed anchorless refund: nothing a rung can bump.
		return false, nil
	}
	sequence := refundTx.TxIn[0].Sequence
	if sequence&(wire.SequenceLockTimeDisabled|wire.SequenceLockTimeIsSeconds) != 0 {
		return false, fmt.Errorf("refund tx for node %s has no block-based timelock", node.ID)
	}
	eligibleAt := uint64(sequence&wire.SequenceLockTimeMask) + node.NodeConfirmationHeight
	if eligibleAt > uint64(blockHeight) {
		return false, nil
	}

	var ladder pb.FeeBumpLadder
	if err := proto.Unmarshal(node.RawRefundLadder, &ladder); err != nil {
		return false, fmt.Errorf("parse refund ladder: %w", err)
	}
	refundTxid := refundTx.TxHash()
	refundVsize := txVsize(refundTx)
	refundValue := refundTx.TxOut[0].Value
	var rungs [][]byte
	var feeRates []int64
	for _, rung := range ladder.GetRungs() {
		rungTx, err := common.TxFromRawTxBytes(rung.GetSignedTx())
		if err != nil || len(rungTx.TxIn) != 1 || len(rungTx.TxOut) != 1 {
			return false, fmt.Errorf("malformed refund ladder rung for node %s", node.ID)
		}
		if rungTx.TxIn[0].PreviousOutPoint.Hash != refundTxid {
			// A ladder left over from an earlier refund of this node.
			return false, nil
		}
		fee := refundValue - rungTx.TxOut[0].Value
		feeRates = append(feeRates, fee*1000/(refundVsize+txVsize(rungTx)))
		rungs = append(rungs, rung.GetSignedTx())
	}
	if len(rungs) == 0 {
		return false, nil
	}

	targetRate, err := estimateFeeRate(btcClient)
	if err != nil {
		// No estimate (regtest, a fresh node): start from the cheapest rung and climb.
		logging.GetLoggerFromContext(ctx).Sugar().Infof("No fee estimate for refund ladder of node %s: %v", node.ID, err)
		targetRate = 0
	}
	rung := pickRefundRung(feeRates, targetRate, uint64(blockHeight)-eligibleAt, refundLadderDefenseWindow)

	err = submitPackage(btcClient, [][]byte{node.RawRefundTx, rungs[rung]})
	if refundTxBroadcastCounter != nil {
		result := "success"
		if err != nil {
			result = "failure"
		}
		refundTxBroadcastCounter.Add(ctx, 1, metric.WithAttributes(
			attribute.String("network", network.String()),
			attribute.String("result", result),
		))
	}
	if err != nil {
		return false, fmt.Errorf("submit refund package (rung %d) for node %s: %w", rung, node.ID, err)
	}
	logging.GetLoggerFromContext(ctx).Sugar().Infof("Submitted refund %s with ladder rung %d of %d for node %s", refundTxid, rung, len(rungs), node.ID)
	return true, nil
}

// pickRefundRung returns the cheapest rung whose package feerate (sat/kvB)
// meets the target, raised as the defense window runs down so that the top
// rung is reached by its end. The climb is by block count alone: the
// watchtower keeps no record of what it already broadcast, and a confirmed
// refund stops the climb by taking the node out of the candidate set.
// ponytail: stateless climb; watch the mempool for a competing stale refund if fee burn matters.
func pickRefundRung(feeRates []int64, targetRate int64, blocksSinceEligible uint64, window uint64) int {
	top := len(feeRates) - 1
	rung := top
	for i, rate := range feeRates {
		if rate >= targetRate {
			rung = i
			break
		}
	}
	if window > 0 {
		if byTime := int(min(blocksSinceEligible, window) * uint64(top) / window); byTime > rung {
			rung = byTime
		}
	}
	return rung
}

// estimateFeeRate is bitcoind's next-block estimate in sat/kvB.
func estimateFeeRate(btcClient bitcoinClient) (int64, error) {
	res, err := btcClient.RawRequest("estimatesmartfee", []json.RawMessage{json.RawMessage("1")})
	if err != nil {
		return 0, err
	}
	var estimate struct {
		FeeRate float64  `json:"feerate"` // BTC/kvB
		Errors  []string `json:"errors"`
	}
	if err := json.Unmarshal(res, &estimate); err != nil {
		return 0, err
	}
	if estimate.FeeRate <= 0 {
		return 0, fmt.Errorf("no fee estimate: %v", estimate.Errors)
	}
	return int64(math.Ceil(estimate.FeeRate * 1e8)), nil
}

func submitPackage(btcClient bitcoinClient, txs [][]byte) error {
	hexTxs := make([]string, len(txs))
	for i, tx := range txs {
		hexTxs[i] = hex.EncodeToString(tx)
	}
	param, err := json.Marshal(hexTxs)
	if err != nil {
		return err
	}
	res, err := btcClient.RawRequest("submitpackage", []json.RawMessage{param})
	if err != nil {
		return err
	}
	var result struct {
		PackageMsg string `json:"package_msg"`
	}
	if err := json.Unmarshal(res, &result); err != nil {
		return err
	}
	if result.PackageMsg != "success" {
		return fmt.Errorf("package rejected: %s", res)
	}
	return nil
}

// txVsize is the BIP-141 virtual size, rounded up.
func txVsize(tx *wire.MsgTx) int64 {
	weight := tx.SerializeSizeStripped()*3 + tx.SerializeSize()
	return int64((weight + 3) / 4)
}
