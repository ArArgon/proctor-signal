package judge

import (
	"crypto/md5"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCompareAll(t *testing.T) {
	expected, err := os.Open("tests/compare/bytes.expected")
	assert.NoError(t, err)

	correct, err := os.Open("tests/compare/bytes.correct")
	assert.NoError(t, err)

	ok, err := compareAll(expected, correct, 1024)
	assert.NoError(t, err)
	assert.True(t, ok)

	_, err = expected.Seek(0, 0)
	assert.NoError(t, err)

	wrong, err := os.Open("tests/compare/bytes.wrong")
	assert.NoError(t, err)

	ok, err = compareAll(expected, wrong, 1024)
	assert.NoError(t, err)
	assert.False(t, ok)
}

func TestCompareFloats(t *testing.T) {
	expected, err := os.Open("tests/compare/float.expected")
	assert.NoError(t, err)

	correct, err := os.Open("tests/compare/float.correct")
	assert.NoError(t, err)

	ok, err := compareFloats(expected, correct, 7)
	assert.NoError(t, err)
	assert.True(t, ok)

	_, err = expected.Seek(0, 0)
	assert.NoError(t, err)

	wrong, err := os.Open("tests/compare/float.wrong")
	assert.NoError(t, err)

	ok, err = compareFloats(expected, wrong, 7)
	assert.NoError(t, err)
	assert.False(t, ok)
}

func TestCompareLines(t *testing.T) {
	expected, err := os.Open("tests/compare/lines.expected")
	assert.NoError(t, err)

	correct, err := os.Open("tests/compare/lines.correct")
	assert.NoError(t, err)

	ok, err := compareLines(expected, correct, false)
	assert.NoError(t, err)
	assert.True(t, ok)

	_, err = expected.Seek(0, 0)
	assert.NoError(t, err)

	wrong, err := os.Open("tests/compare/lines.wrong")
	assert.NoError(t, err)

	ok, err = compareLines(expected, wrong, false)
	assert.NoError(t, err)
	assert.False(t, ok)

	correct = wrong
	assert.NoError(t, err)

	ok, err = compareLines(expected, correct, true)
	assert.NoError(t, err)
	assert.True(t, ok)
}

func TestCompareHash(t *testing.T) {
	md5 := func(data []byte) []byte {
		res := md5.Sum(data)
		return res[:]
	}

	expected, err := os.Open("tests/compare/bytes.expected")
	assert.NoError(t, err)

	correct, err := os.Open("tests/compare/bytes.correct")
	assert.NoError(t, err)

	ok, err := compareHash(expected, correct, 1024, md5)
	assert.NoError(t, err)
	assert.True(t, ok)

	_, err = expected.Seek(0, 0)
	assert.NoError(t, err)

	wrong, err := os.Open("tests/compare/bytes.wrong")
	assert.NoError(t, err)

	ok, err = compareHash(expected, wrong, 1024, md5)
	assert.NoError(t, err)
	assert.False(t, ok)
}
