package utils

import (
	crypto_rand "crypto/rand"
	"encoding/binary"
	"log"
	mathrand "math/rand"

	"github.com/samber/lo"
)

const (
	_alphaTable       = "abcdefghijklmnopqrstuvwxyz"
	_AlphaTable       = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	_numTable         = "0123456789"
	AlphaNumericTable = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
)

func init() {
	var b [8]byte
	_, err := crypto_rand.Read(b[:])
	if err != nil {
		log.Fatalf("random generator init failed: %+v\n", err)
	}
	sd := int64(binary.LittleEndian.Uint64(b[:]))
	mathrand.Seed(sd)
}

// GenerateID generates an ID usually for random files. It is not cryptographically secure.
func GenerateID() string {
	const size = 32
	return lo.RandomString(size, []rune(_alphaTable+_numTable))
}
