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

// RandomStr generates a random string with a given size and table. It is not cryptographically secure.
func RandomStr(size int, table []rune) string {
	tableSize := len(table)
	return string(lo.RepeatBy(size, func(_ int) rune {
		return table[mathrand.Intn(tableSize)]
	}))
}

// GenerateID generates an ID usually for random files. It is not cryptographically secure.
func GenerateID() string {
	const size = 32
	return RandomStr(size, []rune(_alphaTable+_numTable))
}
