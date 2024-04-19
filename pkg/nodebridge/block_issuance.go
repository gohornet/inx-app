package nodebridge

import (
	"context"

	inx "github.com/iotaledger/inx/go"
	"github.com/iotaledger/iota.go/v4/api"
)

// BlockIssuance requests the necessary data to issue a block.
func (n *nodeBridge) BlockIssuance(ctx context.Context, maxParentCount uint32) (*api.IssuanceBlockHeaderResponse, error) {
	resp, err := n.client.ReadBlockIssuance(ctx, &inx.BlockIssuanceRequest{MaxStrongParentsCount: maxParentCount, MaxShallowLikeParentsCount: maxParentCount, MaxWeakParentsCount: maxParentCount})
	if err != nil {
		return nil, err
	}

	latestCommitment, err := resp.UnwrapLatestCommitment(n.APIProvider().CommittedAPI())
	if err != nil {
		return nil, err
	}

	return &api.IssuanceBlockHeaderResponse{
		StrongParents:                resp.UnwrapStrongParents(),
		WeakParents:                  resp.UnwrapWeakParents(),
		ShallowLikeParents:           resp.UnwrapShallowLikeParents(),
		LatestParentBlockIssuingTime: resp.UnwrapLatestParentBlockIssuingTime(),
		LatestFinalizedSlot:          resp.UnwrapLatestFinalizedSlot(),
		LatestCommitment:             latestCommitment,
	}, nil
}
