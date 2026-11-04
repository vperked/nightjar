package agent

import (
	"bytes"
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"time"
)

// The agent's HTTPS key is derived from its token, so the dashboard, which has the same token, knows
// the one key the real agent can present and pins it. Nothing needs copying besides the token, no
// file needs writing on the server, and a machine without the token cannot pose as the agent.

const keyInfo = "nightjar-agent tls key v1"

// identity is the agent's key for token.
func identity(token string) ed25519.PrivateKey {
	seed, err := hkdf.Key(sha256.New, []byte(token), nil, keyInfo, ed25519.SeedSize)
	if err != nil {
		panic(err) // only for an impossible key length
	}
	return ed25519.NewKeyFromSeed(seed)
}

// Certificate is the agent's self-signed certificate for token.
func Certificate(token string) (tls.Certificate, error) {
	key := identity(token)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "nightjar-agent"},
		NotBefore:    time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
		NotAfter:     time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(nil, tmpl, tmpl, key.Public(), key)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, nil
}

// ErrWrongKey means something answered over HTTPS without the key the token gives the real agent.
var ErrWrongKey = errors.New("its certificate does not belong to an agent with this token")

// ClientTLS is the dashboard's side: it accepts only the agent whose key comes from token.
func ClientTLS(token string) *tls.Config {
	want := identity(token).Public().(ed25519.PublicKey)
	return &tls.Config{
		MinVersion: tls.VersionTLS13,
		// The usual checks need a certificate authority; the pinned key below replaces them.
		InsecureSkipVerify: true,
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return ErrWrongKey
			}
			got, ok := cs.PeerCertificates[0].PublicKey.(ed25519.PublicKey)
			if !ok || !bytes.Equal(got, want) {
				return ErrWrongKey
			}
			return nil
		},
	}
}

func serverTLS(token string) (*tls.Config, error) {
	cert, err := Certificate(token)
	if err != nil {
		return nil, err
	}
	return &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{cert}}, nil
}
