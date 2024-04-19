package nodebridge

import (
	"context"
	"io"
	"sync"
	"time"

	grpcretry "github.com/grpc-ecosystem/go-grpc-middleware/retry"
	grpcprometheus "github.com/grpc-ecosystem/go-grpc-prometheus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"github.com/iotaledger/hive.go/ierrors"
	"github.com/iotaledger/hive.go/log"
	"github.com/iotaledger/hive.go/runtime/event"
	"github.com/iotaledger/hive.go/runtime/options"
	inx "github.com/iotaledger/inx/go"
	iotago "github.com/iotaledger/iota.go/v4"
	"github.com/iotaledger/iota.go/v4/api"
	"github.com/iotaledger/iota.go/v4/nodeclient"
)

type NodeBridge interface {
	// Events returns the events.
	Events() *Events
	// Connect connects to the given address and reads the node configuration.
	Connect(ctx context.Context, address string, maxConnectionAttempts uint) error
	// Run starts the node bridge.
	Run(ctx context.Context)
	// Client returns the INXClient.
	Client() inx.INXClient
	// NodeConfig returns the NodeConfiguration.
	NodeConfig() *inx.NodeConfiguration
	// APIProvider returns the APIProvider.
	APIProvider() iotago.APIProvider

	// INXNodeClient returns the NodeClient.
	INXNodeClient() (*nodeclient.Client, error)
	// Management returns the ManagementClient.
	// Returns ErrManagementPluginNotAvailable if the current node does not support the plugin.
	Management(ctx context.Context) (nodeclient.ManagementClient, error)
	// Indexer returns the IndexerClient.
	// Returns ErrIndexerPluginNotAvailable if the current node does not support the plugin.
	Indexer(ctx context.Context) (nodeclient.IndexerClient, error)
	// EventAPI returns the EventAPIClient if supported by the node.
	// Returns ErrMQTTPluginNotAvailable if the current node does not support the plugin.
	EventAPI(ctx context.Context) (*nodeclient.EventAPIClient, error)
	// BlockIssuer returns the BlockIssuerClient.
	// Returns ErrBlockIssuerPluginNotAvailable if the current node does not support the plugin.
	BlockIssuer(ctx context.Context) (nodeclient.BlockIssuerClient, error)

	// ReadIsCandidate returns true if the given account is a candidate.
	ReadIsCandidate(ctx context.Context, id iotago.AccountID, slot iotago.SlotIndex) (bool, error)
	// ReadIsCommitteeMember returns true if the given account is a committee member.
	ReadIsCommitteeMember(ctx context.Context, id iotago.AccountID, slot iotago.SlotIndex) (bool, error)
	// ReadIsValidatorAccount returns true if the given account is a validator account.
	ReadIsValidatorAccount(ctx context.Context, id iotago.AccountID, slot iotago.SlotIndex) (bool, error)

	// RegisterAPIRoute registers the given API route.
	RegisterAPIRoute(ctx context.Context, route string, bindAddress string, path string) error
	// UnregisterAPIRoute unregisters the given API route.
	UnregisterAPIRoute(ctx context.Context, route string) error

	// ActiveRootBlocks returns the active root blocks.
	ActiveRootBlocks(ctx context.Context) (map[iotago.BlockID]iotago.CommitmentID, error)
	// SubmitBlock submits the given block.
	SubmitBlock(ctx context.Context, block *iotago.Block) (iotago.BlockID, error)
	// Block returns the block for the given block ID.
	Block(ctx context.Context, blockID iotago.BlockID) (*iotago.Block, error)
	// BlockMetadata returns the block metadata for the given block ID.
	BlockMetadata(ctx context.Context, blockID iotago.BlockID) (*api.BlockMetadataResponse, error)
	// ListenToBlocks listens to blocks.
	ListenToBlocks(ctx context.Context, consumer func(block *iotago.Block, rawData []byte) error) error
	// ListenToAcceptedBlocks listens to accepted blocks.
	ListenToAcceptedBlocks(ctx context.Context, consumer func(blockMetadata *api.BlockMetadataResponse) error) error
	// ListenToConfirmedBlocks listens to confirmed blocks.
	ListenToConfirmedBlocks(ctx context.Context, consumer func(blockMetadata *api.BlockMetadataResponse) error) error

	// TransactionMetadata returns the transaction metadata for the given transaction ID.
	TransactionMetadata(ctx context.Context, transactionID iotago.TransactionID) (*api.TransactionMetadataResponse, error)

	// Output returns the output with metadata for the given output ID.
	Output(ctx context.Context, outputID iotago.OutputID) (*Output, error)

	// ForceCommitUntil forces the node to commit until the given slot.
	ForceCommitUntil(ctx context.Context, slot iotago.SlotIndex) error
	// Commitment returns the commitment for the given slot.
	Commitment(ctx context.Context, slot iotago.SlotIndex) (*Commitment, error)
	// CommitmentByID returns the commitment for the given commitment ID.
	CommitmentByID(ctx context.Context, id iotago.CommitmentID) (*Commitment, error)
	// ListenToCommitments listens to commitments.
	ListenToCommitments(ctx context.Context, startSlot, endSlot iotago.SlotIndex, consumer func(commitment *Commitment, rawData []byte) error) error

	// ListenToLedgerUpdates listens to ledger updates.
	ListenToLedgerUpdates(ctx context.Context, startSlot, endSlot iotago.SlotIndex, consumer func(update *LedgerUpdate) error) error
	// ListenToAcceptedTransactions listens to accepted transactions.
	ListenToAcceptedTransactions(ctx context.Context, consumer func(tx *AcceptedTransaction) error) error

	// NodeStatus returns the current node status.
	NodeStatus() *inx.NodeStatus
	// IsNodeHealthy returns true if the node is healthy.
	IsNodeHealthy() bool
	// LatestCommitment returns the latest commitment.
	LatestCommitment() *Commitment
	// LatestFinalizedCommitment returns the latest finalized commitment.
	LatestFinalizedCommitment() *Commitment
	// PruningEpoch returns the pruning epoch.
	PruningEpoch() iotago.EpochIndex

	// BlockIssuance requests the necessary data to issue a block.
	BlockIssuance(ctx context.Context, maxParentCount uint32) (*api.IssuanceBlockHeaderResponse, error)
}

var _ NodeBridge = &nodeBridge{}

type nodeBridge struct {
	// the logger used to log events.
	log.Logger

	targetNetworkName string
	events            *Events

	conn        *grpc.ClientConn
	client      inx.INXClient
	nodeConfig  *inx.NodeConfiguration
	apiProvider *iotago.EpochBasedProvider

	nodeStatusMutex           sync.RWMutex
	nodeStatus                *inx.NodeStatus
	latestCommitment          *Commitment
	latestFinalizedCommitment *Commitment
}

type Events struct {
	LatestCommitmentChanged          *event.Event1[*Commitment]
	LatestFinalizedCommitmentChanged *event.Event1[*Commitment]
}

// WithTargetNetworkName checks if the network name of the node is equal to the given targetNetworkName.
// If targetNetworkName is empty, the check is disabled.
func WithTargetNetworkName(targetNetworkName string) options.Option[nodeBridge] {
	return func(n *nodeBridge) {
		n.targetNetworkName = targetNetworkName
	}
}

func New(log log.Logger, opts ...options.Option[nodeBridge]) NodeBridge {
	return options.Apply(&nodeBridge{
		Logger:            log,
		targetNetworkName: "",
		events: &Events{
			LatestCommitmentChanged:          event.New1[*Commitment](),
			LatestFinalizedCommitmentChanged: event.New1[*Commitment](),
		},
		apiProvider: iotago.NewEpochBasedProvider(),
	}, opts)
}

// Events returns the events.
func (n *nodeBridge) Events() *Events {
	return n.events
}

// Connect connects to the given address and reads the node configuration.
func (n *nodeBridge) Connect(ctx context.Context, address string, maxConnectionAttempts uint) error {
	conn, err := grpc.Dial(address,
		grpc.WithChainUnaryInterceptor(grpcretry.UnaryClientInterceptor(), grpcprometheus.UnaryClientInterceptor),
		grpc.WithStreamInterceptor(grpcprometheus.StreamClientInterceptor),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return err
	}
	n.conn = conn
	n.client = inx.NewINXClient(conn)

	retryBackoff := func(_ uint) time.Duration {
		n.LogInfo("> retrying INX connection to node ...")
		return 1 * time.Second
	}

	n.LogInfo("Connecting to node and reading node configuration ...")
	nodeConfig, err := n.client.ReadNodeConfiguration(ctx, &inx.NoParams{}, grpcretry.WithMax(maxConnectionAttempts), grpcretry.WithBackoff(retryBackoff))
	if err != nil {
		return err
	}
	n.nodeConfig = nodeConfig

	n.apiProvider = nodeConfig.APIProvider()

	if n.targetNetworkName != "" {
		// we need to check for the correct target network name
		if n.targetNetworkName != n.APIProvider().CommittedAPI().ProtocolParameters().NetworkName() {
			return ierrors.Errorf("network name mismatch, networkName: \"%s\", targetNetworkName: \"%s\"", n.APIProvider().CommittedAPI().ProtocolParameters().NetworkName(), n.targetNetworkName)
		}
	}

	n.LogInfo("Reading node status ...")
	nodeStatus, err := n.client.ReadNodeStatus(ctx, &inx.NoParams{})
	if err != nil {
		return err
	}

	return n.processNodeStatus(nodeStatus)
}

// Run starts the node bridge.
func (n *nodeBridge) Run(ctx context.Context) {
	c, cancel := context.WithCancel(ctx)

	go func() {
		if err := n.listenToNodeStatus(c); err != nil {
			n.LogErrorf("Error listening to node status: %s", err)
		}
		cancel()
	}()

	<-c.Done()
	_ = n.conn.Close()
}

// Client returns the INXClient.
func (n *nodeBridge) Client() inx.INXClient {
	return n.client
}

// NodeConfig returns the NodeConfiguration.
func (n *nodeBridge) NodeConfig() *inx.NodeConfiguration {
	return n.nodeConfig
}

// APIProvider returns the APIProvider.
func (n *nodeBridge) APIProvider() iotago.APIProvider {
	return n.apiProvider
}

// INXNodeClient returns the NodeClient.
func (n *nodeBridge) INXNodeClient() (*nodeclient.Client, error) {
	return inx.NewNodeclientOverINX(n.client)
}

func (n *nodeBridge) getPluginClient(ctx context.Context, clientInitHook func(ctx context.Context, nodeClient *nodeclient.Client) error, notAvailableError error) error {
	nodeClient, err := n.INXNodeClient()
	if err != nil {
		return err
	}

	initClient := func(ctx context.Context, nodeClient *nodeclient.Client) error {
		ctxTimeout, cancelTimeout := context.WithTimeout(ctx, 1*time.Second)
		defer cancelTimeout()

		return clientInitHook(ctxTimeout, nodeClient)
	}

	// wait until the plugin is available
	for ctx.Err() == nil {
		if err := initClient(ctx, nodeClient); err != nil {
			if !ierrors.Is(err, notAvailableError) {
				return err
			}
			time.Sleep(1 * time.Second)

			continue
		}

		return nil
	}

	return notAvailableError
}

// Management returns the ManagementClient.
// Returns ErrManagementPluginNotAvailable if the current node does not support the plugin.
func (n *nodeBridge) Management(ctx context.Context) (nodeclient.ManagementClient, error) {
	var client nodeclient.ManagementClient

	if err := n.getPluginClient(ctx, func(ctx context.Context, nodeClient *nodeclient.Client) error {
		managementClient, err := nodeClient.Management(ctx)
		if err != nil {
			return err
		}
		client = managementClient

		return nil
	}, nodeclient.ErrManagementPluginNotAvailable); err != nil {
		return nil, err
	}

	return client, nil
}

// Indexer returns the IndexerClient.
// Returns ErrIndexerPluginNotAvailable if the current node does not support the plugin.
func (n *nodeBridge) Indexer(ctx context.Context) (nodeclient.IndexerClient, error) {
	var client nodeclient.IndexerClient

	if err := n.getPluginClient(ctx, func(ctx context.Context, nodeClient *nodeclient.Client) error {
		indexerClient, err := nodeClient.Indexer(ctx)
		if err != nil {
			return err
		}
		client = indexerClient

		return nil
	}, nodeclient.ErrIndexerPluginNotAvailable); err != nil {
		return nil, err
	}

	return client, nil
}

// EventAPI returns the EventAPIClient if supported by the node.
// Returns ErrMQTTPluginNotAvailable if the current node does not support the plugin.
func (n *nodeBridge) EventAPI(ctx context.Context) (*nodeclient.EventAPIClient, error) {
	var client *nodeclient.EventAPIClient

	if err := n.getPluginClient(ctx, func(ctx context.Context, nodeClient *nodeclient.Client) error {
		eventAPIClient, err := nodeClient.EventAPI(ctx)
		if err != nil {
			return err
		}
		client = eventAPIClient

		return nil
	}, nodeclient.ErrMQTTPluginNotAvailable); err != nil {
		return nil, err
	}

	return client, nil
}

// BlockIssuer returns the BlockIssuerClient.
// Returns ErrBlockIssuerPluginNotAvailable if the current node does not support the plugin.
func (n *nodeBridge) BlockIssuer(ctx context.Context) (nodeclient.BlockIssuerClient, error) {
	var client nodeclient.BlockIssuerClient

	if err := n.getPluginClient(ctx, func(ctx context.Context, nodeClient *nodeclient.Client) error {
		blockIssuerClient, err := nodeClient.BlockIssuer(ctx)
		if err != nil {
			return err
		}
		client = blockIssuerClient

		return nil
	}, nodeclient.ErrBlockIssuerPluginNotAvailable); err != nil {
		return nil, err
	}

	return client, nil
}

func ListenToStream[K any](ctx context.Context, receiverFunc func() (K, error), consumerFunc func(K) error) error {
	for {
		item, err := receiverFunc()
		if err != nil {
			if ierrors.Is(err, io.EOF) || status.Code(err) == codes.Canceled {
				// the stream was closed
				break
			}

			return err
		}

		if ctx.Err() != nil {
			// the context was canceled
			break
		}

		if err := consumerFunc(item); err != nil {
			return err
		}
	}

	//nolint:nilerr // false positive
	return nil
}
