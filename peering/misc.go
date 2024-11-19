package peering

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

// MaxPayloadSize caps the message size. It includes 4 bytes of the size
const (
	MaxPayloadSize = math.MaxUint16 - 4
)

func readFrame(stream network.Stream) ([]byte, error) {
	var size uint32

	if err := binary.Read(stream, binary.BigEndian, &size); err != nil {
		return nil, fmt.Errorf("readFrame: failed to read frame size prefix: %w", err)
	}
	if size > MaxPayloadSize {
		return nil, fmt.Errorf("payload size %d exceeds maximum %d bytes", size, MaxPayloadSize)
	}
	if size == 0 {
		return nil, nil
	}
	msgBuf := make([]byte, size)
	if n, err := io.ReadFull(stream, msgBuf); err != nil || n != int(size) {
		if err == nil {
			err = fmt.Errorf("exactly %d bytes expected", size)
		}
		return nil, fmt.Errorf("failed to read frame body: %v", err)
	}

	return msgBuf, nil
}

func writeFrame(stream network.Stream, payload []byte) error {
	if len(payload) > MaxPayloadSize {
		return fmt.Errorf("payload size %d exceeds maximum %d bytes", len(payload), MaxPayloadSize)
	}

	// Allocate a single buffer for the length prefix and payload
	frame := make([]byte, 4+len(payload))
	// Write the length prefix to the first 4 bytes
	binary.BigEndian.PutUint32(frame[:4], uint32(len(payload)))
	// Copy the payload into the buffer after the prefix
	copy(frame[4:], payload)

	// Write the entire frame in one go
	if n, err := stream.Write(frame); err != nil || n != len(frame) {
		if err == nil {
			err = fmt.Errorf("expected %d bytes written", len(frame))
		}
		return fmt.Errorf("failed to write payload: %v", err)
	}
	return nil
}

func ShortPeerIDString(id peer.ID) string {
	s := id.String()

	return ".." + s[len(s)-8:]
}
