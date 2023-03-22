package judge

import (
	"bytes"
	"io"
	"math/rand"
	"regexp"
	"strconv"
	"testing"

	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"

	"proctor-signal/utils"
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

	ok, err := compareAll(expected, correct, 16)
	assert.NoError(t, err)
	assert.True(t, ok)
	reseek(t, expected, correct)

	ok, err = compareAll(expected, wrong, 16)
	assert.NoError(t, err)
	assert.False(t, ok)
	reseek(t, expected, wrong)
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
		t.Run(name+"-ignoreNewline", func(t *testing.T) {
			ok, err := compareLines(expected, actualWithoutNewline, true)
			assert.NoError(t, err)
			assert.True(t, ok)
			reseek(t, expected, actualWithoutNewline)

			ok, err = compareLines(expected, actualWithNewline, true)
			assert.NoError(t, err)
			assert.True(t, ok)
			reseek(t, expected, actualWithNewline)

			ok, err = compareLines(expected, actualWithDifferentEnter, true)
			assert.NoError(t, err)
			assert.True(t, ok)
			reseek(t, expected, actualWithDifferentEnter)
		})

		t.Run(name+"-notIgnoreNewline", func(t *testing.T) {
			ok, err := compareLines(expected, actualWithoutNewline, false)
			assert.NoError(t, err)
			assert.True(t, ok)
			reseek(t, expected, actualWithoutNewline)

			ok, err = compareLines(expected, actualWithNewline, false)
			assert.NoError(t, err)
			assert.False(t, ok)
			reseek(t, expected, actualWithNewline)

			ok, err = compareLines(expected, actualWithDifferentEnter, false)
			assert.NoError(t, err)
			assert.False(t, ok)
			reseek(t, expected, actualWithDifferentEnter)
		})
	}

	// TODO: remove `t.Skip()` when bugs fixed.
	t.Run("buff-test", func(t *testing.T) {
		t.Skip()
		payload := lo.RandomString(4095, []rune(utils.AlphaNumericTable))

		ok, err := compareLines(bytes.NewReader([]byte(payload)), bytes.NewReader([]byte(payload)), true)
		assert.NoError(t, err)
		assert.True(t, ok)

		ok, err = compareLines(
			bytes.NewReader([]byte(payload)),
			bytes.NewReader([]byte(payload+"\r\n")),
			true,
		)
		assert.NoError(t, err)
		assert.True(t, ok)

		ok, err = compareLines(
			bytes.NewReader([]byte(payload+"\r\n")),
			bytes.NewReader([]byte(payload+"\n")),
			false,
		)
		assert.NoError(t, err)
		assert.True(t, ok)
	})

}

func TestCompareLinesNew(t *testing.T) {
	// TODO: remove `t.Skip()` when bugs fixed.
	t.Skip()
	var testcases []string

	genCase := func() string {
		lines := lo.Clamp(rand.Intn(2048), 10, 2048)
		var res string

		// No newline in the end.
		res = lo.RandomString(rand.Intn(1024), []rune(utils.AlphaNumericTable))
		for i := 1; i < lines; i++ {
			lr := (rand.Int() % 2) == 0
			res += lo.Ternary(lr, "\r\n", "\n") +
				lo.RandomString(lo.Clamp(rand.Intn(5120), 1, 5120), []rune(utils.AlphaNumericTable))
		}

		return res
	}

	for i := 0; i < 25; i++ {
		testcases = append(testcases, genCase())
	}

	for idx, text := range testcases {
		label := strconv.Itoa(idx)
		newLine := text + "\n"
		newLineLR := text + "\r\n"

		if regexp.MustCompile(`(\r[^\n])|(\r$)`).MatchString(text) {
			t.Skip(text)
			return
		}

		t.Run("vanilla#"+label, func(t *testing.T) {
			ok, err := compareLines(bytes.NewReader([]byte(text)), bytes.NewReader([]byte(text)), true)
			assert.NoError(t, err)
			assert.True(t, ok)
			ok, err = compareLines(bytes.NewReader([]byte(text)), bytes.NewReader([]byte(text)), false)
			assert.NoError(t, err)
			assert.True(t, ok)
		})

		// ignoreNewline
		t.Run("ignore-newline#"+label, func(t *testing.T) {
			ok, err := compareLines(bytes.NewReader([]byte(text)), bytes.NewReader([]byte(newLine)), true)
			assert.NoError(t, err)
			assert.True(t, ok, "text: %x, newline: %x", text, newLine)
		})
		t.Run("ignore-newline-lr#"+label, func(t *testing.T) {
			ok, err := compareLines(bytes.NewReader([]byte(text)), bytes.NewReader([]byte(newLineLR)), true)
			assert.NoError(t, err)
			assert.True(t, ok, "text: %x, newlineLR: %x", text, newLineLR)
		})

		// notIgnoreNewline
		t.Run("not-ignore-newline#"+label, func(t *testing.T) {
			ok, err := compareLines(bytes.NewReader([]byte(text)), bytes.NewReader([]byte(newLine)), false)
			assert.NoError(t, err)
			assert.Falsef(t, ok, "text: %x, newline: %x", text, newLine)
		})
		t.Run("not-ignore-newline-lr#"+label, func(t *testing.T) {
			ok, err := compareLines(bytes.NewReader([]byte(text)), bytes.NewReader([]byte(newLineLR)), false)
			assert.NoError(t, err)
			assert.Falsef(t, ok, "text: %x, newlineLR: %x", text, newLineLR)
		})
	}

}

func FuzzCompareLines2(f *testing.F) {
	var s1, s2, s3 string
	for i := 0; i < 1024; i++ {
		s1 += "aa\n"
		s2 += "aa\r\n"
		s3 += "\n"
	}
	f.Add(s1)
	f.Add(s1 + "\n")
	f.Add(s2)
	f.Add(s2 + "\r\n")
	f.Add(s3)

	f.Add("")
	f.Add("\n\n\n\n")
	f.Add("\r\n\r\n\r\n")
	f.Add("\n")
	f.Add("\r\n")
	f.Add(" ")
	f.Add(lo.RandomString(4094, []rune(utils.AlphaNumericTable)))
	f.Add(lo.RandomString(4095, []rune(utils.AlphaNumericTable)))
	f.Add(lo.RandomString(4096, []rune(utils.AlphaNumericTable)))

	f.Fuzz(func(t *testing.T, text string) {
		newLine := text + "\n"
		newLineLR := text + "\r\n"

		if regexp.MustCompile(`(\r[^\n])|(\r$)`).MatchString(text) {
			t.Skip(text)
			return
		}

		t.Run("vanilla", func(t *testing.T) {
			ok, err := compareLines2(bytes.NewReader([]byte(text)), bytes.NewReader([]byte(text)), true)
			assert.NoError(t, err)
			assert.True(t, ok)
			ok, err = compareLines2(bytes.NewReader([]byte(text)), bytes.NewReader([]byte(text)), false)
			assert.NoError(t, err)
			assert.True(t, ok)
		})

		// ignoreNewline
		t.Run("ignore-newline", func(t *testing.T) {
			ok, err := compareLines2(bytes.NewReader([]byte(text)), bytes.NewReader([]byte(newLine)), true)
			assert.NoError(t, err)
			assert.True(t, ok, "text: %x, newline: %x", text, newLine)
		})
		t.Run("ignore-newline-lr", func(t *testing.T) {
			ok, err := compareLines2(bytes.NewReader([]byte(text)), bytes.NewReader([]byte(newLineLR)), true)
			assert.NoError(t, err)
			assert.True(t, ok, "text: %x, newlineLR: %x", text, newLineLR)
		})

		// notIgnoreNewline
		t.Run("not-ignore-newline", func(t *testing.T) {
			ok, err := compareLines2(bytes.NewReader([]byte(text)), bytes.NewReader([]byte(newLine)), false)
			assert.NoError(t, err)
			assert.Falsef(t, ok, "text: %x, newline: %x", text, newLine)
		})
		t.Run("not-ignore-newline-rev", func(t *testing.T) {
			ok, err := compareLines2(bytes.NewReader([]byte(newLine)), bytes.NewReader([]byte(text)), false)
			assert.NoError(t, err)
			assert.Falsef(t, ok, "text: %x, expect: %x", newLine, text)
		})
		t.Run("not-ignore-newline-lr", func(t *testing.T) {
			ok, err := compareLines2(bytes.NewReader([]byte(text)), bytes.NewReader([]byte(newLineLR)), false)
			assert.NoError(t, err)
			assert.Falsef(t, ok, "text: %x, newlineLR: %x", text, newLineLR)
		})
		t.Run("not-ignore-newline-lr-rev", func(t *testing.T) {
			ok, err := compareLines2(bytes.NewReader([]byte(newLineLR)), bytes.NewReader([]byte(text)), false)
			assert.NoError(t, err)
			assert.Falsef(t, ok, "text: %x, expect: %x", newLineLR, text)
		})
	})
}
