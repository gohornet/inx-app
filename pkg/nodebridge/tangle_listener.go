package nodebridge

import (
	"context"
	"sync"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/iotaledger/hive.go/ierrors"
	"github.com/iotaledger/hive.go/log"
	"github.com/iotaledger/hive.go/runtime/event"
	"github.com/iotaledger/hive.go/runtime/valuenotifier"
	inx "github.com/iotaledger/inx/go"
	iotago "github.com/iotaledger/iota.go/v4"
	"github.com/iotaledger/iota.go/v4/api"
)

// ErrAlreadyRegistered is returned when a callback for the same ID has already been registered.
var ErrAlreadyRegistered = ierrors.New("callback is already registered")

type TangleListener struct {
	log.Logger

	nodeBridge                  NodeBridge
	blockAcceptedNotifier       *valuenotifier.Notifier[iotago.BlockID]
	commitmentConfirmedNotifier *valuenotifier.Notifier[iotago.SlotIndex]

	blockAcceptedCallbacks     map[iotago.BlockID]BlockAcceptedCallback
	blockAcceptedCallbacksLock sync.Mutex

	Events *TangleListenerEvents
}

type TangleListenerEvents struct {
	BlockAccepted *event.Event1[*api.BlockMetadataResponse]
}

type BlockAcceptedCallback = func(*api.BlockMetadataResponse)

func NewTangleListener(logger log.Logger, nodeBridge NodeBridge) *TangleListener {
	return &TangleListener{
		Logger:                      logger,
		nodeBridge:                  nodeBridge,
		blockAcceptedNotifier:       valuenotifier.New[iotago.BlockID](),
		commitmentConfirmedNotifier: valuenotifier.New[iotago.SlotIndex](),
		blockAcceptedCallbacks:      map[iotago.BlockID]BlockAcceptedCallback{},
		Events: &TangleListenerEvents{
			BlockAccepted: event.New1[*api.BlockMetadataResponse](),
		},
	}
}

// RegisterBlockAcceptedCallback registers a callback for when a block with blockID becomes accepted.
// If another callback for the same ID has already been registered, an error is returned.
func (t *TangleListener) RegisterBlockAcceptedCallback(ctx context.Context, blockID iotago.BlockID, f BlockAcceptedCallback) error {
	if err := t.registerBlockAcceptedCallback(blockID, f); err != nil {
		return err
	}

	metadata, err := t.nodeBridge.BlockMetadata(ctx, blockID)
	if err != nil {
		// if the block is not found, then it is also not yet accepted
		if status.Code(err) == codes.NotFound {
			return nil
		}

		return err
	}

	if metadata.BlockState == api.BlockStateAccepted ||
		metadata.BlockState == api.BlockStateConfirmed ||
		metadata.BlockState == api.BlockStateFinalized {
		// trigger the callback, because the block is already accepted
		t.triggerBlockAcceptedCallback(metadata)
	}

	return nil
}

func (t *TangleListener) registerBlockAcceptedCallback(blockID iotago.BlockID, f BlockAcceptedCallback) error {
	t.blockAcceptedCallbacksLock.Lock()
	defer t.blockAcceptedCallbacksLock.Unlock()

	if _, ok := t.blockAcceptedCallbacks[blockID]; ok {
		return ierrors.Wrapf(ErrAlreadyRegistered, "block %s", blockID)
	}
	t.blockAcceptedCallbacks[blockID] = f

	return nil
}

// DeregisterBlockAcceptedCallback removes a previously registered callback for blockID.
func (t *TangleListener) DeregisterBlockAcceptedCallback(blockID iotago.BlockID) {
	t.blockAcceptedCallbacksLock.Lock()
	defer t.blockAcceptedCallbacksLock.Unlock()
	delete(t.blockAcceptedCallbacks, blockID)
}

// ClearBlockAcceptedCallbacks removes all previously registered blockAcceptedCallbacks.
func (t *TangleListener) ClearBlockAcceptedCallbacks() {
	t.blockAcceptedCallbacksLock.Lock()
	defer t.blockAcceptedCallbacksLock.Unlock()
	t.blockAcceptedCallbacks = map[iotago.BlockID]BlockAcceptedCallback{}
}

func (t *TangleListener) triggerBlockAcceptedCallback(metadata *api.BlockMetadataResponse) {
	t.blockAcceptedCallbacksLock.Lock()
	defer t.blockAcceptedCallbacksLock.Unlock()
	if f, ok := t.blockAcceptedCallbacks[metadata.BlockID]; ok {
		go f(metadata)
		delete(t.blockAcceptedCallbacks, metadata.BlockID)
	}
}

// RegisterBlockAcceptedEvent registers an event for when the block with blockID becomes accepted.
// If the block is already accepted, the event is triggered immediately.
func (t *TangleListener) RegisterBlockAcceptedEvent(ctx context.Context, blockID iotago.BlockID) (*valuenotifier.Listener, error) {
	blockAcceptedListener := t.blockAcceptedNotifier.Listener(blockID)

	// check if the block is already accepted
	metadata, err := t.nodeBridge.BlockMetadata(ctx, blockID)
	if err != nil {
		// if the block is not found, then it is also not yet accepted
		if status.Code(err) == codes.NotFound {
			return blockAcceptedListener, nil
		}

		// in case of another error, we need to deregister the listener
		blockAcceptedListener.Deregister()

		return nil, err
	}

	if metadata.BlockState == api.BlockStateAccepted ||
		metadata.BlockState == api.BlockStateConfirmed ||
		metadata.BlockState == api.BlockStateFinalized {
		// trigger the sync event, because the block is already accepted
		t.blockAcceptedNotifier.Notify(metadata.BlockID)
	}

	return blockAcceptedListener, nil
}

// RegisterSlotConfirmedEvent registers an event for when the slot with sIndex gets confirmed.
// If the slot is already confirmed, the event is triggered immediately.
func (t *TangleListener) RegisterSlotConfirmedEvent(slot iotago.SlotIndex) *valuenotifier.Listener {
	slotConfirmedListener := t.commitmentConfirmedNotifier.Listener(slot)

	// check if the slot is already confirmed
	if latestConfirmedSlot := t.nodeBridge.NodeStatus().GetLastConfirmedBlockSlot(); iotago.SlotIndex(latestConfirmedSlot) >= slot {
		// trigger the sync event, because the slot is already confirmed
		t.commitmentConfirmedNotifier.Notify(slot)
	}

	return slotConfirmedListener
}

func (t *TangleListener) Run(ctx context.Context) {
	c, cancel := context.WithCancel(ctx)
	defer cancel()

	go func() {
		if err := t.listenToAcceptedBlocks(c, cancel); err != nil {
			t.LogErrorf("Error listening to accepted blocks: %s", err.Error())
		}
	}()

	hook := t.nodeBridge.Events().LatestFinalizedCommitmentChanged.Hook(func(c *Commitment) {
		t.commitmentConfirmedNotifier.Notify(c.Commitment.Slot)
	})
	defer hook.Unhook()
	<-c.Done()
}

func (t *TangleListener) listenToAcceptedBlocks(ctx context.Context, cancel context.CancelFunc) error {
	defer cancel()

	stream, err := t.nodeBridge.Client().ListenToAcceptedBlocks(ctx, &inx.NoParams{})
	if err != nil {
		return err
	}

	if err := ListenToStream(ctx, stream.Recv, func(inxMetadata *inx.BlockMetadata) error {
		metadata := inxMetadata.Unwrap()

		t.triggerBlockAcceptedCallback(metadata)
		t.blockAcceptedNotifier.Notify(metadata.BlockID)
		t.Events.BlockAccepted.Trigger(metadata)

		return nil
	}); err != nil {
		t.LogErrorf("listenToAcceptedBlocks failed: %s", err.Error())
		return err
	}

	return nil
}
