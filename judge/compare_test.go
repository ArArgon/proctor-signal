package judge

import (
	"bytes"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
)

func reseek(t *testing.T, seekers ...io.Seeker) {
	for _, seeker := range seekers {
		_, err := seeker.Seek(0, 0)
		assert.NoError(t, err)
	}
}

func TestCompareAll(t *testing.T) {
	expected := bytes.NewReader([]byte("12345\n67890\nabcdefg\nhijklmn\nopqrst\nuvwxyz\n"))
	correct := bytes.NewReader([]byte("12345\n67890\nabcdefg\nhijklmn\nopqrst\nuvwxyz\n"))
	wrong := bytes.NewReader([]byte("12345\n67890\nabcdefg\nhijklmn\nopqrst\nuvwxyz"))

	for name, hashFunc := range hashFuncs {
		t.Run(name, func(t *testing.T) {
			ok, err := compareAll(expected, correct, 16, hashFunc)
			assert.NoError(t, err)
			assert.True(t, ok)
			reseek(t, expected, correct)

			ok, err = compareAll(expected, wrong, 16, hashFunc)
			assert.NoError(t, err)
			assert.False(t, ok)
			reseek(t, expected, wrong)
		})
	}
}

func TestCompareFloats(t *testing.T) {
	expected := bytes.NewReader([]byte("3.1415926"))
	correct := bytes.NewReader([]byte("3.14159265"))
	wrong := bytes.NewReader([]byte("3.1415928"))

	ok, err := compareFloats(expected, correct, 7)
	assert.NoError(t, err)
	assert.True(t, ok)
	reseek(t, expected, correct)

	ok, err = compareFloats(expected, wrong, 7)
	assert.NoError(t, err)
	assert.False(t, ok)
	reseek(t, expected, correct)
}

func TestCompareLines(t *testing.T) {
	expectedTestcases := map[string]*bytes.Reader{
		"expectedWithNewline":        bytes.NewReader([]byte("12345\n67890\nabcdefg\nhijklmn\nopq rst\nuvw xyz\n")),
		"expectedWithoutNewline":     bytes.NewReader([]byte("12345\n67890\nabcdefg\nhijklmn\nopq rst\nuvw xyz")),
		"expectedWithDifferentEnter": bytes.NewReader([]byte("12345\n67890\r\nabcdefg\nhijklmn\r\nopq rst\nuvw xyz")),
	}

	actualWithNewline := bytes.NewReader([]byte("12345\n67890\nabcdefg\nhijklmn\nopq rst\nuvw xyz\n"))
	actualWithoutNewline := bytes.NewReader([]byte("12345\n67890\nabcdefg\nhijklmn\nopq rst\nuvw xyz"))
	actualWithDifferentEnter := bytes.NewReader([]byte("12345\n67890\r\nabcdefg\nhijklmn\r\nopq rst\nuvw xyz\r\n"))

	for name, expected := range expectedTestcases {
		t.Run(name, func(t *testing.T) {
			ignoreNewline := true
			t.Run("ignoreNewline", func(t *testing.T) {
				for name, hashFunc := range hashFuncs {
					t.Run(name, func(t *testing.T) {
						ok, err := compareLines(expected, actualWithoutNewline, ignoreNewline, hashFunc)
						assert.NoError(t, err)
						assert.True(t, ok)
						reseek(t, expected, actualWithoutNewline)

						ok, err = compareLines(expected, actualWithNewline, ignoreNewline, hashFunc)
						assert.NoError(t, err)
						assert.True(t, ok)
						reseek(t, expected, actualWithNewline)

						ok, err = compareLines(expected, actualWithDifferentEnter, ignoreNewline, hashFunc)
						assert.NoError(t, err)
						assert.True(t, ok)
						reseek(t, expected, actualWithDifferentEnter)
					})
				}
			})

			ignoreNewline = false
			t.Run("notIgnoreNewline", func(t *testing.T) {
				for name, hashFunc := range hashFuncs {
					t.Run(name, func(t *testing.T) {
						ok, err := compareLines(expected, actualWithoutNewline, ignoreNewline, hashFunc)
						assert.NoError(t, err)
						assert.True(t, ok)
						reseek(t, expected, actualWithoutNewline)

						ok, err = compareLines(expected, actualWithNewline, ignoreNewline, hashFunc)
						assert.NoError(t, err)
						assert.False(t, ok)
						reseek(t, expected, actualWithNewline)

						ok, err = compareLines(expected, actualWithDifferentEnter, ignoreNewline, hashFunc)
						assert.NoError(t, err)
						assert.False(t, ok)
						reseek(t, expected, actualWithDifferentEnter)
					})
				}
			})
		})
	}
}
