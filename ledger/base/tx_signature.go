package base

import (
	"crypto/ed25519"
	"fmt"

	"github.com/lunfardo314/proxima/util"
)

type Signature struct {
	SignatureType  byte
	SignatureBytes []byte
}

const (
	SignatureTypeED25519 = byte(0)
)

// SignatureFromBytes parses signature data
// - first byte is signature type. 0 - is of ED25519 signature
// - bytes 1:... are the full signature bytes, that includes proper signature and the public key, depending on the type
func SignatureFromBytes(data []byte) (*Signature, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("SignatureFromBytes: empty data")
	}
	switch data[0] {
	case SignatureTypeED25519:
		if len(data) != 1+ed25519.PrivateKeySize+ed25519.PublicKeySize {
			return nil, fmt.Errorf("SignatureFromBytes: wrong ED25519 signature data size: must be %d+1, got %d",
				ed25519.PrivateKeySize+ed25519.PublicKeySize, len(data))
		}
	default:
		return nil, fmt.Errorf("SignatureFromBytes: unknown signature type '%d'", data[0])
	}
	return &Signature{
		SignatureType:  data[0],
		SignatureBytes: data[1:],
	}, nil
}

// ED25519

func (s *Signature) MustPubicKeyED25519() ed25519.PublicKey {
	util.Assertf(s.SignatureType == SignatureTypeED25519, "SignatureType ED25519 is expected")
	return s.SignatureBytes[ed25519.SignatureSize : ed25519.SignatureSize+ed25519.PublicKeySize]
}

func (s *Signature) MustSignatureDataED25519() []byte {
	util.Assertf(s.SignatureType == SignatureTypeED25519, "SignatureType ED25519 is expected")
	return s.SignatureBytes[:ed25519.SignatureSize]
}
