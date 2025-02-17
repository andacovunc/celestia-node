package getters

import (
	"context"
	"testing"
	"time"

	"github.com/ipfs/go-datastore"
	ds_sync "github.com/ipfs/go-datastore/sync"
	routinghelpers "github.com/libp2p/go-libp2p-routing-helpers"
	"github.com/libp2p/go-libp2p/core/host"
	routingdisc "github.com/libp2p/go-libp2p/p2p/discovery/routing"
	"github.com/libp2p/go-libp2p/p2p/net/conngater"
	mocknet "github.com/libp2p/go-libp2p/p2p/net/mock"
	"github.com/stretchr/testify/require"

	"github.com/celestiaorg/celestia-app/pkg/da"
	libhead "github.com/celestiaorg/go-header"
	"github.com/celestiaorg/nmt/namespace"
	"github.com/celestiaorg/rsmt2d"

	"github.com/celestiaorg/celestia-node/header"
	"github.com/celestiaorg/celestia-node/header/headertest"
	"github.com/celestiaorg/celestia-node/share"
	"github.com/celestiaorg/celestia-node/share/availability/discovery"
	"github.com/celestiaorg/celestia-node/share/eds"
	"github.com/celestiaorg/celestia-node/share/p2p/peers"
	"github.com/celestiaorg/celestia-node/share/p2p/shrexeds"
	"github.com/celestiaorg/celestia-node/share/p2p/shrexnd"
	"github.com/celestiaorg/celestia-node/share/p2p/shrexsub"
)

func TestShrexGetter(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
	t.Cleanup(cancel)

	// create test net
	net, err := mocknet.FullMeshConnected(2)
	require.NoError(t, err)
	clHost, srvHost := net.Hosts()[0], net.Hosts()[1]

	// launch eds store and put test data into it
	edsStore, err := newStore(t)
	require.NoError(t, err)
	err = edsStore.Start(ctx)
	require.NoError(t, err)

	ndClient, _ := newNDClientServer(ctx, t, edsStore, srvHost, clHost)
	edsClient, _ := newEDSClientServer(ctx, t, edsStore, srvHost, clHost)

	// create shrex Getter
	sub := new(headertest.Subscriber)
	peerManager, err := testManager(ctx, clHost, sub)
	require.NoError(t, err)
	getter := NewShrexGetter(edsClient, ndClient, peerManager)
	require.NoError(t, getter.Start(ctx))

	t.Run("ND_Available", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(ctx, time.Second)
		t.Cleanup(cancel)

		// generate test data
		eds, dah, nID := generateTestEDS(t)
		require.NoError(t, edsStore.Put(ctx, dah.Hash(), eds))
		peerManager.Validate(ctx, srvHost.ID(), shrexsub.Notification{
			DataHash: dah.Hash(),
			Height:   1,
		})

		got, err := getter.GetSharesByNamespace(ctx, &dah, nID)
		require.NoError(t, err)
		require.NoError(t, got.Verify(&dah, nID))
	})

	t.Run("ND_err_not_found", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(ctx, time.Second)
		t.Cleanup(cancel)

		// generate test data
		_, dah, nID := generateTestEDS(t)
		peerManager.Validate(ctx, srvHost.ID(), shrexsub.Notification{
			DataHash: dah.Hash(),
			Height:   1,
		})

		_, err := getter.GetSharesByNamespace(ctx, &dah, nID)
		require.ErrorIs(t, err, share.ErrNotFound)
	})

	t.Run("EDS_Available", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(ctx, time.Second)
		t.Cleanup(cancel)

		// generate test data
		eds, dah, _ := generateTestEDS(t)
		require.NoError(t, edsStore.Put(ctx, dah.Hash(), eds))
		peerManager.Validate(ctx, srvHost.ID(), shrexsub.Notification{
			DataHash: dah.Hash(),
			Height:   1,
		})

		got, err := getter.GetEDS(ctx, &dah)
		require.NoError(t, err)
		require.Equal(t, eds.Flattened(), got.Flattened())
	})

	t.Run("EDS_ctx_deadline", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(ctx, time.Second)

		// generate test data
		_, dah, _ := generateTestEDS(t)
		peerManager.Validate(ctx, srvHost.ID(), shrexsub.Notification{
			DataHash: dah.Hash(),
			Height:   1,
		})

		cancel()
		_, err := getter.GetEDS(ctx, &dah)
		require.ErrorIs(t, err, context.Canceled)
	})

	t.Run("EDS_err_not_found", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(ctx, time.Second)
		t.Cleanup(cancel)

		// generate test data
		_, dah, _ := generateTestEDS(t)
		peerManager.Validate(ctx, srvHost.ID(), shrexsub.Notification{
			DataHash: dah.Hash(),
			Height:   1,
		})

		_, err := getter.GetEDS(ctx, &dah)
		require.ErrorIs(t, err, share.ErrNotFound)
	})
}

func newStore(t *testing.T) (*eds.Store, error) {
	t.Helper()

	tmpDir := t.TempDir()
	ds := ds_sync.MutexWrap(datastore.NewMapDatastore())
	return eds.NewStore(tmpDir, ds)
}

func generateTestEDS(t *testing.T) (*rsmt2d.ExtendedDataSquare, da.DataAvailabilityHeader, namespace.ID) {
	eds := share.RandEDS(t, 4)
	dah := da.NewDataAvailabilityHeader(eds)
	randNID := dah.RowsRoots[(len(dah.RowsRoots)-1)/2][:8]
	return eds, dah, randNID
}

func testManager(ctx context.Context, host host.Host, headerSub libhead.Subscriber[*header.ExtendedHeader],
) (*peers.Manager, error) {
	shrexSub, err := shrexsub.NewPubSub(ctx, host, "test")
	if err != nil {
		return nil, err
	}

	disc := discovery.NewDiscovery(nil,
		routingdisc.NewRoutingDiscovery(routinghelpers.Null{}), 0, time.Second, time.Second)
	connGater, err := conngater.NewBasicConnectionGater(ds_sync.MutexWrap(datastore.NewMapDatastore()))
	if err != nil {
		return nil, err
	}
	manager, err := peers.NewManager(
		peers.DefaultParameters(),
		headerSub,
		shrexSub,
		disc,
		host,
		connGater,
	)
	return manager, err
}

func newNDClientServer(ctx context.Context, t *testing.T, edsStore *eds.Store, srvHost, clHost host.Host,
) (*shrexnd.Client, *shrexnd.Server) {
	params := shrexnd.DefaultParameters()

	// create server and register handler
	server, err := shrexnd.NewServer(params, srvHost, edsStore, NewStoreGetter(edsStore))
	require.NoError(t, err)
	require.NoError(t, server.Start(ctx))

	t.Cleanup(func() {
		_ = server.Stop(ctx)
	})

	// create client and connect it to server
	client, err := shrexnd.NewClient(params, clHost)
	require.NoError(t, err)
	return client, server
}

func newEDSClientServer(ctx context.Context, t *testing.T, edsStore *eds.Store, srvHost, clHost host.Host,
) (*shrexeds.Client, *shrexeds.Server) {
	params := shrexeds.DefaultParameters()

	// create server and register handler
	server, err := shrexeds.NewServer(params, srvHost, edsStore)
	require.NoError(t, err)
	require.NoError(t, server.Start(ctx))

	t.Cleanup(func() {
		_ = server.Stop(ctx)
	})

	// create client and connect it to server
	client, err := shrexeds.NewClient(params, clHost)
	require.NoError(t, err)
	return client, server
}
