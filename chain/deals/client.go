package deals

import (
	"context"
	"io/ioutil"
	"os"
	"sync/atomic"

	"github.com/ipfs/go-cid"
	logging "github.com/ipfs/go-log"
	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/peer"

	"github.com/filecoin-project/go-lotus/chain/address"
	"github.com/filecoin-project/go-lotus/chain/store"
	"github.com/filecoin-project/go-lotus/chain/types"
	"github.com/filecoin-project/go-lotus/lib/cborrpc"
	"github.com/filecoin-project/go-lotus/lib/sectorbuilder"
)

var log = logging.Logger("deals")

const ProtocolID = "/fil/storage/mk/1.0.0"

type DealStatus int

const (
	DealResolvingMiner = DealStatus(iota)
)

type Deal struct {
	ID     uint64
	Status DealStatus
	Miner  peer.ID
}

type Client struct {
	cs *store.ChainStore
	sb *sectorbuilder.SectorBuilder
	h  host.Host

	next  uint64
	deals map[uint64]Deal

	incoming chan Deal

	stop    chan struct{}
	stopped chan struct{}
}

func NewClient(cs *store.ChainStore) *Client {
	c := &Client{
		cs: cs,

		deals: map[uint64]Deal{},

		incoming: make(chan Deal, 16),

		stop:    make(chan struct{}),
		stopped: make(chan struct{}),
	}

	return c
}

func (c *Client) Run() {
	go func() {
		defer close(c.stopped)

		for {
			select {
			case deal := <-c.incoming:
				log.Info("incoming deal")

				// TODO: track in datastore
				c.deals[deal.ID] = deal

			case <-c.stop:
				return
			}
		}
	}()
}

func (c *Client) Start(ctx context.Context, data cid.Cid, totalPrice types.BigInt, from address.Address, miner address.Address, minerID peer.ID, blocksDuration uint64) (uint64, error) {
	// TODO: Eww
	f, err := ioutil.TempFile(os.TempDir(), "commP-temp-")
	if err != nil {
		return 0, err
	}
	_, err = f.Write([]byte("hello\n"))
	if err != nil {
		return 0, err
	}
	if err := f.Close(); err != nil {
		return 0, err
	}
	commP, err := c.sb.GeneratePieceCommitment(f.Name(), 6)
	if err != nil {
		return 0, err
	}
	if err := os.Remove(f.Name()); err != nil {
		return 0, err
	}

	// TODO: use data
	proposal := StorageDealProposal{
		PieceRef:          "bafkqabtimvwgy3yk", // identity 'hello\n'
		SerializationMode: SerializationRaw,
		CommP:             commP[:],
		Size:              6,
		TotalPrice:        totalPrice,
		Duration:          blocksDuration,
		Payment:           nil, // TODO
		MinerAddress:      miner,
		ClientAddress:     from,
	}

	s, err := c.h.NewStream(ctx, minerID, ProtocolID)
	if err != nil {
		return 0, err
	}
	defer s.Close() // TODO: not too soon?

	log.Info("Sending deal proposal")

	signedProposal := &SignedStorageDealProposal{
		Proposal:  proposal,
		Signature: nil, // TODO: SIGN!
	}

	if err := cborrpc.WriteCborRPC(s, signedProposal); err != nil {
		return 0, err
	}

	var resp SignedStorageDealResponse
	if err := cborrpc.ReadCborRPC(s, &resp); err != nil {
		log.Errorw("failed to read StorageDealResponse message", "error", err)
		return 0, err
	}

	id := atomic.AddUint64(&c.next, 1)
	deal := Deal{
		ID:     id,
		Status: DealResolvingMiner,
		Miner:  minerID,
	}

	c.incoming <- deal
	return id, nil
}

func (c *Client) Stop() {
	close(c.stop)
	<-c.stopped
}