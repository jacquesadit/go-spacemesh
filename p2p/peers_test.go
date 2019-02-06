package p2p

import (
	"github.com/spacemeshos/go-spacemesh/p2p/p2pcrypto"
	"github.com/spacemeshos/go-spacemesh/p2p/service"
	"github.com/stretchr/testify/assert"
	"sync/atomic"
	"testing"
	"time"
)

func getPeers(p Service) (Peers, chan p2pcrypto.PublicKey, chan p2pcrypto.PublicKey) {
	value := atomic.Value{}
	value.Store(make([]Peer, 0, 20))
	pi := &PeersImpl{snapshot: &value, exit: make(chan struct{})}
	new, expierd := p.SubscribePeerEvents()
	go pi.listenToPeers(new, expierd)
	return pi, new, expierd
}

func TestPeers_GetPeers(t *testing.T) {
	pi, new, _ := getPeers(service.NewSimulator().NewNode())
	a := p2pcrypto.NewRandomPubkey()
	new <- a
	time.Sleep(10 * time.Millisecond) //allow context switch
	peers := pi.GetPeers()
	defer pi.Close()
	assert.True(t, len(peers) == 1, "number of peers incorrect")
	assert.True(t, peers[0] == a, "returned wrong peer")
}

func TestPeers_Close(t *testing.T) {
	pi, new, _ := getPeers(service.NewSimulator().NewNode())
	a := p2pcrypto.NewRandomPubkey()
	new <- a
	time.Sleep(10 * time.Millisecond) //allow context switch
	pi.Close()
	//_, ok := <-new
	//assert.True(t, !ok, "channel 'new' still open")
	//_, ok = <-expierd
	//assert.True(t, !ok, "channel 'expierd' still open")
}

func TestPeers_AddPeer(t *testing.T) {
	pi, new, _ := getPeers(service.NewSimulator().NewNode())
	a := p2pcrypto.NewRandomPubkey()
	b := p2pcrypto.NewRandomPubkey()
	c := p2pcrypto.NewRandomPubkey()
	d := p2pcrypto.NewRandomPubkey()
	e := p2pcrypto.NewRandomPubkey()
	new <- a
	time.Sleep(10 * time.Millisecond) //allow context switch
	peers := pi.GetPeers()
	assert.True(t, len(peers) == 1, "number of peers incorrect, length was ", len(peers))
	new <- b
	new <- c
	new <- d
	new <- e
	defer pi.Close()
	time.Sleep(10 * time.Millisecond) //allow context switch
	peers = pi.GetPeers()
	assert.True(t, len(peers) == 5, "number of peers incorrect, length was ", len(peers))
}

func TestPeers_RemovePeer(t *testing.T) {
	pi, new, expierd := getPeers(service.NewSimulator().NewNode())
	a := p2pcrypto.NewRandomPubkey()
	b := p2pcrypto.NewRandomPubkey()
	c := p2pcrypto.NewRandomPubkey()
	d := p2pcrypto.NewRandomPubkey()
	e := p2pcrypto.NewRandomPubkey()
	new <- a
	time.Sleep(10 * time.Millisecond) //allow context switch
	peers := pi.GetPeers()
	assert.True(t, len(peers) == 1, "number of peers incorrect, length was ", len(peers))
	new <- b
	new <- c
	new <- d
	new <- e
	defer pi.Close()
	time.Sleep(10 * time.Millisecond) //allow context switch
	peers = pi.GetPeers()
	assert.True(t, len(peers) == 5, "number of peers incorrect, length was ", len(peers))
	expierd <- b
	expierd <- c
	expierd <- d
	expierd <- e
	time.Sleep(10 * time.Millisecond) //allow context switch
	peers = pi.GetPeers()
	assert.True(t, len(peers) == 1, "number of peers incorrect, length was ", len(peers))
}