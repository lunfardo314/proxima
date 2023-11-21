package peering

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/unitrie/common"
)

func encodePeerMsgPull(txids ...core.TransactionID) []byte {
	util.Assertf(len(txids) < math.MaxUint16, "too many transaction IDs")

	var buf bytes.Buffer
	var size [2]byte
	binary.BigEndian.PutUint16(size[:], uint16(len(txids)))
	buf.Write(size[:])
	for i := range txids {
		buf.Write(txids[i][:])
	}
	return buf.Bytes()
}

func decodePeerMsgPull(data []byte) ([]core.TransactionID, error) {
	if len(data) < 2 {
		return nil, fmt.Errorf("not a QueryTransactions message")
	}
	ret := make([]core.TransactionID, binary.BigEndian.Uint16(data[:2]))
	rdr := bytes.NewReader(data[2:])
	var txid [core.TransactionIDLength]byte
	for i := range ret {
		n, err := rdr.Read(txid[:])
		if err != nil || n != core.TransactionIDLength {
			return nil, fmt.Errorf("DecodePeerMessageQueryTransactions: wrong msg data")
		}
		ret[i], err = core.TransactionIDFromBytes(txid[:])
		common.AssertNoError(err)
	}
	return ret, nil
}

func readFrame(stream network.Stream) ([]byte, error) {
	var sizeBuf [4]byte

	if n, err := io.ReadFull(stream, sizeBuf[:]); err != nil || n != 4 {
		if err == nil {
			err = errors.New("exactly 4 bytes expected")
		}
		return nil, fmt.Errorf("failed to read frame size prefix: %v", err)
	}
	size := binary.BigEndian.Uint32(sizeBuf[:])
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
	util.Assertf(len(payload) < math.MaxUint32, "len(payload) < math.MaxUint32")
	var sizeBuf [4]byte

	binary.BigEndian.PutUint32(sizeBuf[:], uint32(len(payload)))
	if n, err := stream.Write(sizeBuf[:]); err != nil || n != 4 {
		if err == nil {
			err = errors.New("expected 4 bytes written")
		}
		return fmt.Errorf("failed to write size prefix: %v", err)
	}
	if len(payload) == 0 {
		return nil
	}
	if n, err := stream.Write(payload); err != nil || n != len(payload) {
		if err == nil {
			err = fmt.Errorf("expected %d bytes written", len(payload))
		}
		return fmt.Errorf("failed to write size prefix: %v", err)
	}
	return nil
}
