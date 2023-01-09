package utils

import (
	mathrand "math/rand"
	"time"

	"github.com/samber/lo"
)

const (
	_alphaTable       = "abcdefghijklmnopqrstuvwxyz"
	_AlphaTable       = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	_numTable         = "0123456789"
	AlphaNumericTable = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
)

func init() {
	mathrand.Seed(time.Now().UnixNano())
}

// GenerateID generates an ID usually for random files. It is not cryptographically secure.
func GenerateID() string {
	const size = 32
	return lo.RandomString(size, []rune(_alphaTable+_numTable))
}
