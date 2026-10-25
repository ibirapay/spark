package handler

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	pbspark "github.com/lightsparkdev/spark/proto/spark"
	pbinternal "github.com/lightsparkdev/spark/proto/spark_internal"
	sparktesting "github.com/lightsparkdev/spark/testing"
	"github.com/stretchr/testify/require"
)

func TestClaimInstantValidateDecisionAgainstPrepare(t *testing.T) {
	t.Parallel()
	handler := NewClaimInstantStaticDepositFlowHandler(sparktesting.TestConfig(t))
	primary := uuid.NewString()
	prepare := &pbinternal.ClaimInstantStaticDepositUtxoSwapPrepareRequest{
		OriginalRequest: &pbinternal.ClaimInstantStaticDepositUtxoSwapRequest{TransferId: strings.ToUpper(primary)}, // non-canonical
	}

	require.NoError(t, handler.ValidateDecisionAgainstPrepare(prepare, &pbinternal.ClaimInstantStaticDepositUtxoSwapCommitRequest{TransferId: primary}))
	require.NoError(t, handler.ValidateDecisionAgainstPrepare(prepare, &pbinternal.ClaimInstantStaticDepositUtxoSwapRollbackRequest{TransferId: primary}))
	require.NoError(t, handler.ValidateDecisionAgainstPrepare(prepare, &pbinternal.ClaimInstantStaticDepositUtxoSwapPrepareRequest{
		OriginalRequest: &pbinternal.ClaimInstantStaticDepositUtxoSwapRequest{TransferId: primary},
	}))

	other := uuid.NewString()
	require.ErrorContains(t, handler.ValidateDecisionAgainstPrepare(prepare, &pbinternal.ClaimInstantStaticDepositUtxoSwapCommitRequest{TransferId: other}), "does not match the prepared transfer id")
	require.ErrorContains(t, handler.ValidateDecisionAgainstPrepare(prepare, &pbinternal.ClaimInstantStaticDepositUtxoSwapRollbackRequest{TransferId: other}), "does not match the prepared transfer id")
	require.ErrorContains(t, handler.ValidateDecisionAgainstPrepare(prepare, &pbinternal.ClaimInstantStaticDepositUtxoSwapPrepareRequest{
		OriginalRequest: &pbinternal.ClaimInstantStaticDepositUtxoSwapRequest{TransferId: other},
	}), "does not match the prepared transfer id")
	// A secondary transfer commit the prepare never carried.
	require.ErrorContains(t, handler.ValidateDecisionAgainstPrepare(prepare, &pbinternal.ClaimInstantStaticDepositUtxoSwapCommitRequest{
		TransferId: primary, TransferCommit: &pbinternal.SendTransferCommitRequest{TransferId: other},
	}), "secondary transfer commit")

	secondary := uuid.NewString()
	withSecondary := &pbinternal.ClaimInstantStaticDepositUtxoSwapPrepareRequest{
		OriginalRequest: &pbinternal.ClaimInstantStaticDepositUtxoSwapRequest{
			TransferId: primary,
			Transfer:   &pbspark.StartTransferRequest{TransferId: secondary},
		},
	}
	require.NoError(t, handler.ValidateDecisionAgainstPrepare(withSecondary, &pbinternal.ClaimInstantStaticDepositUtxoSwapCommitRequest{
		TransferId: primary, TransferCommit: &pbinternal.SendTransferCommitRequest{TransferId: secondary},
	}))
	require.ErrorContains(t, handler.ValidateDecisionAgainstPrepare(withSecondary, &pbinternal.ClaimInstantStaticDepositUtxoSwapCommitRequest{
		TransferId: primary, TransferCommit: &pbinternal.SendTransferCommitRequest{TransferId: other},
	}), "does not match the prepared secondary transfer id")
	require.ErrorContains(t, handler.ValidateDecisionAgainstPrepare(withSecondary, &pbinternal.ClaimInstantStaticDepositUtxoSwapCommitRequest{TransferId: primary}), "secondary transfer commit")

	require.ErrorContains(t, handler.ValidateDecisionAgainstPrepare(&pbinternal.SendTransferPrepareRequest{}, &pbinternal.ClaimInstantStaticDepositUtxoSwapCommitRequest{}), "unexpected prepare op type")
	require.ErrorContains(t, handler.ValidateDecisionAgainstPrepare(prepare, &pbinternal.SendTransferCommitRequest{}), "unexpected decision op type")
}

func TestReserveInstantValidateDecisionAgainstPrepare(t *testing.T) {
	t.Parallel()
	handler := NewReserveInstantStaticDepositFlowHandler(sparktesting.TestConfig(t))
	primary := uuid.NewString()
	prepare := &pbinternal.ReserveInstantStaticDepositUtxoSwapPrepareRequest{
		OriginalRequest: &pbinternal.ReserveInstantStaticDepositUtxoSwapRequest{
			Transfer: &pbspark.StartTransferRequest{TransferId: strings.ToUpper(primary)}, // non-canonical
		},
	}
	commitFor := func(id string) *pbinternal.ReserveInstantStaticDepositUtxoSwapCommitRequest {
		return &pbinternal.ReserveInstantStaticDepositUtxoSwapCommitRequest{TransferCommit: &pbinternal.SendTransferCommitRequest{TransferId: id}}
	}

	require.NoError(t, handler.ValidateDecisionAgainstPrepare(prepare, commitFor(primary)))
	require.NoError(t, handler.ValidateDecisionAgainstPrepare(prepare, &pbinternal.ReserveInstantStaticDepositUtxoSwapRollbackRequest{RequestedTransferId: primary}))
	require.NoError(t, handler.ValidateDecisionAgainstPrepare(prepare, &pbinternal.ReserveInstantStaticDepositUtxoSwapPrepareRequest{
		OriginalRequest: &pbinternal.ReserveInstantStaticDepositUtxoSwapRequest{Transfer: &pbspark.StartTransferRequest{TransferId: primary}},
	}))

	other := uuid.NewString()
	require.ErrorContains(t, handler.ValidateDecisionAgainstPrepare(prepare, commitFor(other)), "does not match the prepared transfer id")
	require.ErrorContains(t, handler.ValidateDecisionAgainstPrepare(prepare, &pbinternal.ReserveInstantStaticDepositUtxoSwapCommitRequest{}), "does not match the prepared transfer id")
	require.ErrorContains(t, handler.ValidateDecisionAgainstPrepare(prepare, &pbinternal.ReserveInstantStaticDepositUtxoSwapRollbackRequest{RequestedTransferId: other}), "does not match the prepared transfer id")
	require.ErrorContains(t, handler.ValidateDecisionAgainstPrepare(prepare, &pbinternal.ReserveInstantStaticDepositUtxoSwapPrepareRequest{
		OriginalRequest: &pbinternal.ReserveInstantStaticDepositUtxoSwapRequest{Transfer: &pbspark.StartTransferRequest{TransferId: other}},
	}), "does not match the prepared transfer id")
	require.ErrorContains(t, handler.ValidateDecisionAgainstPrepare(&pbinternal.SendTransferPrepareRequest{}, commitFor(primary)), "unexpected prepare op type")
	require.ErrorContains(t, handler.ValidateDecisionAgainstPrepare(prepare, &pbinternal.SendTransferCommitRequest{}), "unexpected decision op type")
}

func TestStaticDepositUtxoSwapValidateDecisionAgainstPrepare(t *testing.T) {
	t.Parallel()
	handler := NewStaticDepositUtxoSwapFlowHandler(sparktesting.TestConfig(t))
	transferID := uuid.NewString()
	outpoint := func() *pbspark.UTXO {
		return &pbspark.UTXO{Txid: []byte{1, 2, 3, 4}, Vout: 1, Network: pbspark.Network_REGTEST}
	}
	prepare := &pbinternal.StaticDepositUtxoSwapPrepareRequest{
		OriginalRequest: &pbinternal.InitiateStaticDepositUtxoSwapRequest{
			OnChainUtxo: outpoint(),
			Transfer:    &pbspark.StartTransferRequest{TransferId: strings.ToUpper(transferID)}, // non-canonical
		},
	}
	commitFor := func(utxo *pbspark.UTXO, id string) *pbinternal.StaticDepositUtxoSwapCommitRequest {
		return &pbinternal.StaticDepositUtxoSwapCommitRequest{OnChainUtxo: utxo, TransferCommit: &pbinternal.SendTransferCommitRequest{TransferId: id}}
	}

	require.NoError(t, handler.ValidateDecisionAgainstPrepare(prepare, commitFor(outpoint(), transferID)))
	require.NoError(t, handler.ValidateDecisionAgainstPrepare(prepare, &pbinternal.StaticDepositUtxoSwapRollbackRequest{OnChainUtxo: outpoint()}))
	require.NoError(t, handler.ValidateDecisionAgainstPrepare(prepare, &pbinternal.StaticDepositUtxoSwapPrepareRequest{
		OriginalRequest: &pbinternal.InitiateStaticDepositUtxoSwapRequest{OnChainUtxo: outpoint()},
	}))

	wrongTxid, wrongVout, wrongNetwork := outpoint(), outpoint(), outpoint()
	wrongTxid.Txid = []byte{9, 9, 9, 9}
	wrongVout.Vout = 2
	wrongNetwork.Network = pbspark.Network_MAINNET
	for _, utxo := range []*pbspark.UTXO{wrongTxid, wrongVout, wrongNetwork, {Vout: 1, Network: pbspark.Network_REGTEST}, nil} {
		require.ErrorContains(t, handler.ValidateDecisionAgainstPrepare(prepare, commitFor(utxo, transferID)), "does not match the prepared utxo")
		require.ErrorContains(t, handler.ValidateDecisionAgainstPrepare(prepare, &pbinternal.StaticDepositUtxoSwapRollbackRequest{OnChainUtxo: utxo}), "does not match the prepared utxo")
	}
	require.ErrorContains(t, handler.ValidateDecisionAgainstPrepare(prepare, commitFor(outpoint(), uuid.NewString())), "does not match the prepared transfer id")
	require.ErrorContains(t, handler.ValidateDecisionAgainstPrepare(prepare, &pbinternal.StaticDepositUtxoSwapCommitRequest{OnChainUtxo: outpoint()}), "does not match the prepared transfer id")

	// Both sides without a txid must not pass as "equal".
	noTxid := &pbinternal.StaticDepositUtxoSwapPrepareRequest{
		OriginalRequest: &pbinternal.InitiateStaticDepositUtxoSwapRequest{OnChainUtxo: &pbspark.UTXO{Vout: 1, Network: pbspark.Network_REGTEST}},
	}
	require.ErrorContains(t, handler.ValidateDecisionAgainstPrepare(noTxid, &pbinternal.StaticDepositUtxoSwapRollbackRequest{OnChainUtxo: &pbspark.UTXO{Vout: 1, Network: pbspark.Network_REGTEST}}), "does not match the prepared utxo")

	require.ErrorContains(t, handler.ValidateDecisionAgainstPrepare(&pbinternal.SendTransferPrepareRequest{}, commitFor(outpoint(), transferID)), "unexpected prepare op type")
	require.ErrorContains(t, handler.ValidateDecisionAgainstPrepare(prepare, &pbinternal.SendTransferCommitRequest{}), "unexpected decision op type")
}
