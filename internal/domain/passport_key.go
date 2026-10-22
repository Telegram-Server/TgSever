package domain

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
)

func NormalizePassportPublicKey(text string) (string, int, error) {
	block, rest := pem.Decode([]byte(text))
	if block == nil || len(rest) != 0 {
		return "", 0, ErrPassportInvalid
	}
	var key *rsa.PublicKey
	if block.Type == "RSA PUBLIC KEY" {
		v, e := x509.ParsePKCS1PublicKey(block.Bytes)
		if e != nil {
			return "", 0, ErrPassportInvalid
		}
		key = v
	} else {
		v, e := x509.ParsePKIXPublicKey(block.Bytes)
		if e != nil {
			return "", 0, ErrPassportInvalid
		}
		var ok bool
		key, ok = v.(*rsa.PublicKey)
		if !ok {
			return "", 0, ErrPassportInvalid
		}
	}
	if key.N.BitLen() < 2048 || key.N.BitLen() > 4096 || key.E < 3 {
		return "", 0, ErrPassportInvalid
	}
	b, e := x509.MarshalPKIXPublicKey(key)
	if e != nil {
		return "", 0, e
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: b})), key.Size(), nil
}
