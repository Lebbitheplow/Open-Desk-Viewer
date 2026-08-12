package auth

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"math/big"
)

// parseX509Cert decodes a base64-encoded X509 certificate from the x5c field
func parseX509Cert(b64Cert string) (*x509.Certificate, error) {
	decoded, err := base64.StdEncoding.DecodeString(b64Cert)
	if err != nil {
		return nil, fmt.Errorf("failed to decode base64 cert: %w", err)
	}
	cert, err := x509.ParseCertificate(decoded)
	if err != nil {
		return nil, fmt.Errorf("failed to parse x509 certificate: %w", err)
	}
	return cert, nil
}

// decodeJWKToPublicKey decodes a JWK with n/e fields into an RSA public key
func decodeJWKToPublicKey(jwk JWK) (*rsa.PublicKey, error) {
	nBytes, err := base64.RawURLEncoding.DecodeString(jwk.N)
	if err != nil {
		return nil, fmt.Errorf("failed to decode n: %w", err)
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(jwk.E)
	if err != nil {
		return nil, fmt.Errorf("failed to decode e: %w", err)
	}

	// e is typically 3 bytes (01 00 01) but can be 1-3 bytes
	eInt := 0
	for _, b := range eBytes {
		eInt = (eInt << 8) | int(b)
	}

	n := new(big.Int).SetBytes(nBytes)
	pubKey := &rsa.PublicKey{
		N: n,
		E: eInt,
	}

	// Basic validation
	if n.Sign() <= 0 {
		return nil, fmt.Errorf("invalid RSA modulus")
	}
	if eInt < 3 || eInt%2 == 0 {
		return nil, fmt.Errorf("invalid RSA exponent")
	}

	return pubKey, nil
}

// ParsePEMPublicKey parses a PEM-encoded RSA public key
func ParsePEMPublicKey(pemData []byte) (*rsa.PublicKey, error) {
	block, _ := pem.Decode(pemData)
	if block == nil {
		return nil, fmt.Errorf("failed to decode PEM block")
	}

	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse PKIX public key: %w", err)
	}

	rsaPub, ok := pub.(*rsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("not an RSA public key")
	}

	return rsaPub, nil
}
