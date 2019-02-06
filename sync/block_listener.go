package sync

import (
	"bytes"
	"github.com/spacemeshos/go-spacemesh/log"
	"github.com/spacemeshos/go-spacemesh/mesh"
	"github.com/spacemeshos/go-spacemesh/p2p"
	"github.com/spacemeshos/go-spacemesh/p2p/config"
	"github.com/spacemeshos/go-spacemesh/p2p/server"
	"github.com/spacemeshos/go-spacemesh/p2p/service"
	"sync/atomic"
	"time"
)

type MessageServer server.MessageServer

const BlockProtocol = "/blocks/1.0/"
const NewBlockProtocol = "newBlock"

type BlockListener struct {
	*server.MessageServer
	p2p.Peers
	*mesh.Mesh
	BlockValidator
	log.Log
	bufferSize           int
	semaphore            chan struct{}
	unknownQueue         chan mesh.BlockID //todo consider benefits of changing to stack
	receivedGossipBlocks chan service.GossipMessage
	startLock            uint32
	timeout              time.Duration
	exit                 chan struct{}
}

func (bl *BlockListener) Close() {
	close(bl.exit)
}

func (bl *BlockListener) Start() {
	if atomic.CompareAndSwapUint32(&bl.startLock, 0, 1) {
		go bl.run()
		go bl.ListenToGossipBlocks()
	}
}

func (bl *BlockListener) OnNewBlock(b *mesh.Block) {
	bl.addUnknownToQueue(b)
}

func NewBlockListener(net server.Service, bv BlockValidator, layers *mesh.Mesh, timeout time.Duration, concurrency int, logger log.Log) *BlockListener {
	bl := BlockListener{
		BlockValidator:       bv,
		Mesh:                 layers,
		Peers:                p2p.NewPeers(net),
		MessageServer:        server.NewMsgServer(net, BlockProtocol, timeout, make(chan service.DirectMessage, config.ConfigValues.BufferSize), logger),
		Log:                  logger,
		semaphore:            make(chan struct{}, concurrency),
		unknownQueue:         make(chan mesh.BlockID, 200), //todo tune buffer size + get buffer from config
		exit:                 make(chan struct{}),
		receivedGossipBlocks: net.RegisterGossipProtocol(NewBlockProtocol),
	}
	bl.RegisterMsgHandler(BLOCK, newBlockRequestHandler(layers, logger))

	return &bl
}

func (bl *BlockListener) ListenToGossipBlocks() {
	for {
		select {
		case <-bl.exit:
			bl.Logger.Info("listening  stopped")
			return
		case data := <-bl.receivedGossipBlocks:
			blk, err := mesh.BytesAsBlock(bytes.NewReader(data.Bytes()))
			if err != nil {
				log.Error("received invalid block %v", data.Bytes()[:7])
				data.ReportValidation(NewBlockProtocol, false)
				break
			}
			if bl.EligibleBlock(&blk) {
				data.ReportValidation(NewBlockProtocol, true)
				err := bl.AddBlock(&blk)
				if err != nil {
					log.Info("Block already received")
					break
				}
				bl.addUnknownToQueue(&blk)
			} else {
				data.ReportValidation(NewBlockProtocol, false)
			}

		}
	}
}

func (bl *BlockListener) run() {
	for {
		select {
		case <-bl.exit:
			bl.Log.Info("run stopped")
			return
		case id := <-bl.unknownQueue:
			bl.Log.Debug("fetch block ", id, "buffer is at ", len(bl.unknownQueue)/cap(bl.unknownQueue), " capacity")
			bl.semaphore <- struct{}{}
			go func() {
				defer func() { <-bl.semaphore }()
				bl.FetchBlock(id)
			}()
		}
	}
}

//todo handle case where no peer knows the block
func (bl *BlockListener) FetchBlock(id mesh.BlockID) {
	for _, p := range bl.GetPeers() {
		if ch, err := sendBlockRequest(bl.MessageServer, p, id, bl.Log); err == nil {
			if b := <-ch; b != nil && bl.EligibleBlock(b) {
				bl.AddBlock(b)
				bl.addUnknownToQueue(b) //add all child blocks to unknown queue
				return
			}
		}
	}
}

func (bl *BlockListener) addUnknownToQueue(b *mesh.Block) {
	for _, block := range b.ViewEdges {
		//if unknown block
		if _, err := bl.GetBlock(block); err != nil {
			bl.unknownQueue <- block
		}
	}
}