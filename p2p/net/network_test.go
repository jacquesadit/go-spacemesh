package net

import (
	"github.com/spacemeshos/go-spacemesh/p2p/config"
	"github.com/spacemeshos/go-spacemesh/p2p/p2pcrypto"
	"github.com/spacemeshos/go-spacemesh/p2p/node"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"math/rand"
	"sync"
	"testing"
	"time"
)

func waitForCallbackOrTimeout(t *testing.T, outchan chan NewConnectionEvent, expectedPeerPubkey p2pcrypto.PublicKey) {
	select {
	case res := <-outchan:
		assert.Equal(t, expectedPeerPubkey.String(), res.Conn.Session().ID().String(), "wrong session received")
	case <-time.After(2 * time.Second):
		assert.Nil(t, expectedPeerPubkey, "Didn't get channel notification")
	}
}

func Test_sumByteArray(t *testing.T) {
	bytez := sumByteArray([]byte{0x1, 0x1, 0x1, 0x1, 0x1, 0x1, 0x1, 0x1, 0x1, 0x1, 0x1, 0x1, 0x1, 0x1, 0x1, 0x1, 0x1, 0x1, 0x1, 0x1})
	assert.Equal(t, bytez, uint(20))
	bytez2 := sumByteArray([]byte{0x5, 0x5, 0x5, 0x5, 0x5, 0x5, 0x5, 0x5, 0x5, 0x5, 0x5, 0x5, 0x5, 0x5, 0x5, 0x5, 0x5, 0x5, 0x5, 0x5})
	assert.Equal(t, bytez2, uint(100))
}

func TestNet_EnqueueMessage(t *testing.T) {
	testnodes := 100
	cfg := config.DefaultConfig()
	ln, err := node.NewNodeIdentity(cfg, "0.0.0.0:0000", false)
	assert.NoError(t, err)
	n, err := NewNet(cfg, ln)
	assert.NoError(t, err)
	rnd := rand.New(rand.NewSource(time.Now().UnixNano()))
	var wg sync.WaitGroup
	for i := 0; i < testnodes; i++ {
		wg.Add(1)
		go func() {
			rnode := node.GenerateRandomNodeData()
			sum := sumByteArray(rnode.PublicKey().Bytes())
			msg := make([]byte, 10, 10)
			rnd.Read(msg)
			n.EnqueueMessage(IncomingMessageEvent{NewConnectionMock(rnode.PublicKey()), msg})
			s := <-n.IncomingMessages()[sum%n.queuesCount]
			assert.Equal(t, s.Message, msg)
			assert.Equal(t, s.Conn.RemotePublicKey(), rnode.PublicKey())
			wg.Done()
		}()
	}
	wg.Wait()
}

func TestHandlePreSessionIncomingMessage(t *testing.T) {
	r := require.New(t)
	var wg sync.WaitGroup

	aliceNode, _ := node.GenerateTestNode(t)
	bobNode, _ := node.GenerateTestNode(t)

	bobsAliceConn := NewConnectionMock(aliceNode.PublicKey())
	bobsAliceConn.addr = aliceNode.Address()
	bobsNet, err := NewNet(config.DefaultConfig(), bobNode)
	r.NoError(err)
	bobsNewConnChan := bobsNet.SubscribeOnNewRemoteConnections()

	aliceSessionWithBob := createSession(aliceNode.PrivateKey(), bobNode.PublicKey())
	aliceHandshakeMessageToBob, err := generateHandshakeMessage(aliceSessionWithBob, 1, 123, aliceNode.PublicKey())
	r.NoError(err)

	err = bobsNet.HandlePreSessionIncomingMessage(bobsAliceConn, aliceHandshakeMessageToBob)
	r.NoError(err)
	r.Equal(int32(0), bobsAliceConn.SendCount())

	wg.Add(1)
	go func() {
		wg.Done()
		waitForCallbackOrTimeout(t, bobsNewConnChan, aliceNode.PublicKey())
	}()

	wg.Wait()

	err = bobsNet.HandlePreSessionIncomingMessage(bobsAliceConn, aliceHandshakeMessageToBob)
	r.NoError(err)
	r.Equal(int32(0), bobsAliceConn.SendCount())

	wg.Add(1)
	go func() {
		wg.Done()
		waitForCallbackOrTimeout(t, bobsNewConnChan, aliceNode.PublicKey())
	}()
	wg.Wait()
}
