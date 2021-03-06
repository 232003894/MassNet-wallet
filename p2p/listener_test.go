// +build !network

package p2p

import (
	"bytes"
	"github.com/massnetorg/MassNet-wallet/consensus"
	"testing"
)

func TestListener(t *testing.T) {
	consensus.SkipCI(t)
	// Create a listener
	l, _ := NewDefaultListener("tcp", ":43480", true)

	// Dial the listener
	lAddr := l.ExternalAddress()
	connOut, err := lAddr.Dial()
	if err != nil {
		t.Fatalf("Could not connect to listener address %v", lAddr)
	} else {
		t.Logf("Created a connection to listener address %v", lAddr)
	}
	connIn, ok := <-l.Connections()
	if !ok {
		t.Fatalf("Could not get inbound connection from listener")
	}

	msg := []byte("hi!")
	go connIn.Write(msg)
	b := make([]byte, 32)
	n, err := connOut.Read(b)
	if err != nil {
		t.Fatalf("Error reading off connection: %v", err)
	}

	b = b[:n]
	if !bytes.Equal(msg, b) {
		t.Fatalf("Got %s, expected %s", b, msg)
	}

	// Close the server, no longer needed.
	l.Stop()
}
