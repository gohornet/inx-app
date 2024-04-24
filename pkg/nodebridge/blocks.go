package nodebridge

import (
	"context"

	inx "github.com/iotaledger/inx/go"
	iotago "github.com/iotaledger/iota.go/v4"
	"github.com/iotaledger/iota.go/v4/api"
)

// ActiveRootBlocks returns the active root blocks.
func (n *nodeBridge) ActiveRootBlocks(ctx context.Context) (map[iotago.BlockID]iotago.CommitmentID, error) {
	response, err := n.client.ReadActiveRootBlocks(ctx, &inx.NoParams{})
	if err != nil {
		return nil, err
	}

	return response.Unwrap(), nil
}

// SubmitBlock submits the given block.
func (n *nodeBridge) SubmitBlock(ctx context.Context, block *iotago.Block) (iotago.BlockID, error) {
	blk, err := inx.WrapBlock(block)
	if err != nil {
		return iotago.BlockID{}, err
	}

	response, err := n.client.SubmitBlock(ctx, blk)
	if err != nil {
		return iotago.BlockID{}, err
	}

	return response.Unwrap(), nil
}

// Block returns the block for the given block ID.
func (n *nodeBridge) Block(ctx context.Context, blockID iotago.BlockID) (*iotago.Block, error) {
	inxBlock, err := n.client.ReadBlock(ctx, inx.NewBlockId(blockID))
	if err != nil {
		return nil, err
	}

	return inxBlock.UnwrapBlock(n.apiProvider)
}

// BlockMetadata returns the block metadata for the given block ID.
func (n *nodeBridge) BlockMetadata(ctx context.Context, blockID iotago.BlockID) (*api.BlockMetadataResponse, error) {
	inxBlockMetadata, err := n.client.ReadBlockMetadata(ctx, inx.NewBlockId(blockID))
	if err != nil {
		return nil, err
	}

	return inxBlockMetadata.Unwrap(), nil
}

// ListenToBlocks listens to blocks.
func (n *nodeBridge) ListenToBlocks(ctx context.Context, consumer func(block *iotago.Block, rawData []byte) error) error {
	stream, err := n.client.ListenToBlocks(ctx, &inx.NoParams{})
	if err != nil {
		return err
	}

	if err := ListenToStream(ctx, stream.Recv, func(block *inx.Block) error {
		return consumer(block.MustUnwrapBlock(n.apiProvider), block.GetBlock().GetData())
	}); err != nil {
		n.LogErrorf("ListenToBlocks failed: %s", err.Error())
		return err
	}

	return nil
}

// ListenToBlockMetadata listens to block metadata changes (pending, accepted, confirmed, dropped).
func (n *nodeBridge) ListenToBlockMetadata(ctx context.Context, consumer func(*api.BlockMetadataResponse) error) error {
	stream, err := n.client.ListenToBlockMetadata(ctx, &inx.NoParams{})
	if err != nil {
		return err
	}

	if err := ListenToStream(ctx, stream.Recv, func(inxBlockMetadata *inx.BlockMetadata) error {
		return consumer(inxBlockMetadata.Unwrap())
	}); err != nil {
		n.LogErrorf("ListenToBlockMetadata failed: %s", err.Error())
		return err
	}

	return nil
}
