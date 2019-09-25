package hare

import (
	"errors"
	"github.com/spacemeshos/go-spacemesh/log"
	"github.com/spacemeshos/go-spacemesh/p2p/service"
	"sync"
)

const InboxCapacity = 1024 // inbox size per instance

type StartInstanceError error

type syncStateFunc func() bool

type Validator interface {
	Validate(m *Msg) bool
}

// Closer is used to add closeability to an object
type Closer struct {
	channel chan struct{} // closeable go routines listen to this channel
}

func NewCloser() Closer {
	return Closer{make(chan struct{})}
}

// Closes all listening instances (should be called only once)
func (closer *Closer) Close() {
	close(closer.channel)
}

// CloseChannel returns the channel to wait on
func (closer *Closer) CloseChannel() chan struct{} {
	return closer.channel
}

const (
	invalid = false
	valid   = true
)

// Broker is responsible for dispatching hare Messages to the matching set objectId listener
type Broker struct {
	Closer
	log.Log
	network        NetworkService
	eValidator     Validator
	stateQuerier   StateQuerier
	isNodeSynced   syncStateFunc
	layersPerEpoch uint16
	inbox          chan service.GossipMessage
	mtx            sync.RWMutex
	layerState     map[InstanceId]bool
	outbox         map[InstanceId]chan *Msg
	pending        map[InstanceId][]*Msg
	tasks          chan func()
	latestLayer    InstanceId
	isStarted      bool
}

func NewBroker(networkService NetworkService, eValidator Validator, stateQuerier StateQuerier, syncState syncStateFunc, layersPerEpoch uint16, closer Closer, log log.Log) *Broker {
	return &Broker{
		Closer:         closer,
		Log:            log,
		network:        networkService,
		eValidator:     eValidator,
		stateQuerier:   stateQuerier,
		isNodeSynced:   syncState,
		layersPerEpoch: layersPerEpoch,
		layerState:     make(map[InstanceId]bool),
		outbox:         make(map[InstanceId]chan *Msg),
		pending:        make(map[InstanceId][]*Msg),
		tasks:          make(chan func()),
		latestLayer:    0,
	}
}

// Start listening to protocol Messages and dispatch Messages (non-blocking)
func (b *Broker) Start() error {
	if b.isStarted { // Start has been called at least twice
		b.Error("Could not start instance")
		return StartInstanceError(errors.New("instance already started"))
	}

	b.isStarted = true

	b.inbox = b.network.RegisterGossipProtocol(protoName)
	go b.eventLoop()

	return nil
}

// Dispatch incoming Messages to the matching set objectId instance
func (b *Broker) eventLoop() {
	//slots := make(chan struct{}, 200)
	//for i := 0; i < 200; i++ {
	//	slots <- struct{}{}
	//}
	for {
		select {
		case msg := <-b.inbox:
			//<-slots
			go func() {
				//defer func() { slots <- struct{}{} }()
				if msg == nil {
					b.Error("Message validation failed: called with nil")
					return
				}

				hareMsg, err := MessageFromBuffer(msg.Bytes())
				if err != nil {
					b.Error("Could not build message err=%v", err)
					return
				}

				// InnerMsg validation
				if hareMsg.InnerMsg == nil {
					b.Warning("Message validation failed: InnerMsg is nil")
					return
				}
				b.mtx.RLock()
				msgInstId := InstanceId(hareMsg.InnerMsg.InstanceId)
				state, exist := b.layerState[msgInstId]
				if exist && state == invalid { // invalid instance, ignore
					if !b.isSynced(msgInstId) {
						b.With().Debug("Ignoring message because the layer is out of sync",
							log.Uint64("layer_id", uint64(msgInstId)))
					} else {
						b.With().Debug("Ignoring message because the layer has already unregistered",
							log.Uint64("layer_id", uint64(msgInstId)))
					}
					b.mtx.RUnlock()
					return
				}
				c, ok := b.outbox[msgInstId]
				if !ok { // unknown instance, maybe just an early msg
					if b.latestLayer+1 != msgInstId { // not an early msg
						// ignore msg
						b.With().Warning("Message validation failed: message for unregistered layer and is not an early message",
							log.Uint64("current_layer", uint64(b.latestLayer)), log.Uint64("msg_layer", uint64(msgInstId)))
						b.mtx.RUnlock()
						return
					}

					// check synchronicity
					if !b.isSynced(msgInstId) {
						// not synced, we will ignore messages for this instance from now on
						b.Info("layer %v is not synced, ignoring message", msgInstId)
						b.mtx.RUnlock()
						return
					}

					// early msg, should validate & buffer
					// but we do it later to avoid duplication
				}
				b.mtx.RUnlock()
				// the msg is either early or has instance

				// create msg
				iMsg, err := newMsg(hareMsg, b.stateQuerier, b.layersPerEpoch)
				if err != nil {
					b.Warning("Message validation failed: could not construct msg err=%v", err)
					return
				}

				// validate msg
				if !b.eValidator.Validate(iMsg) {
					b.Warning("Message validation failed: eValidator returned false %v", hareMsg)
					return
				}

				// validation passed, report
				msg.ReportValidation(protoName)

				// early msg
				if !ok {
					b.mtx.Lock()
					if _, exist := b.pending[msgInstId]; !exist { // create buffer if first msg
						b.pending[msgInstId] = make([]*Msg, 0)
					}
					// we want to write all buffered messages to a chan with InboxCapacity len
					// hence, we limit the buffer for pending messages
					if len(b.pending[msgInstId]) == InboxCapacity {
						b.Error("Reached %v pending messages. Ignoring message for layer %v sent from %v",
							InboxCapacity, msgInstId, iMsg.PubKey.ShortString())
						b.mtx.Unlock()
						return

					}
					b.pending[msgInstId] = append(b.pending[msgInstId], iMsg)
					b.mtx.Unlock()
					return
				}

				// has instance, just send
				c <- iMsg
			}()
		case <-b.CloseChannel():
			b.Warning("Broker exiting")
			return
		}
	}
}

func (b *Broker) updateLatestLayer(id InstanceId) {
	if id <= b.latestLayer { // should expect to update only newer layers
		b.Error("Tried to update a previous layer expected %v > %v", id, b.latestLayer)
		return
	}

	b.latestLayer = id
}

func (b *Broker) updateSynchronicity(id InstanceId) {
	if _, ok := b.layerState[id]; ok { // already has result
		return
	}

	// not exist means unknown, check & set

	if !b.isNodeSynced() {
		b.Info("Note: node is not synced. Marking layer %v as invalid", id)
		b.layerState[id] = invalid // mark invalid
		return
	}

	b.layerState[id] = valid // mark valid
}

func (b *Broker) isSynced(id InstanceId) bool {
	b.updateSynchronicity(id)

	state, ok := b.layerState[id]
	if !ok { // not exist means unknown
		log.Panic("layerState doesn't contain a value after call to updateSynchronicity")
	}

	return state == valid
}

// Register a listener to Messages
// Note: the registering instance is assumed to be started and accepting Messages
func (b *Broker) Register(id InstanceId) (chan *Msg, error) {
	b.mtx.Lock()
	defer b.mtx.Unlock()
	b.updateLatestLayer(id)

	if !b.isSynced(id) {
		return nil, errors.New("not synced")
	}
	b.outbox[id] = make(chan *Msg, InboxCapacity*5)

	pendingForInstance := b.pending[id]
	if pendingForInstance != nil {
		for _, mOut := range pendingForInstance {
			b.outbox[id] <- mOut
		}
		delete(b.pending, id)
	}

	return b.outbox[id], nil

}

// Unregister a listener
func (b *Broker) Unregister(id InstanceId) {
	b.mtx.Lock()
	b.layerState[id] = invalid // mark unregistered (invalid)
	delete(b.outbox, id)       // delete outbox for if
	b.mtx.Unlock()
	b.Info("Unregistered layer %v ", id)
}
