package judge

import (
	"bufio"
	"bytes"
	"crypto/md5"
	"crypto/sha256"
	"errors"
	"fmt"
	"hash"
	"io"
	"math"
	"reflect"
	"strconv"
)

type compareFunc func(expected, actual []byte) (bool, error)

const newline = '\n'

func getMd5() compareFunc {
	expectedHash := md5.New()
	actualHash := md5.New()
	return func(expected, actual []byte) (bool, error) {
		if _, err := expectedHash.Write(expected); err != nil {
			return false, err
		}
		if _, err := actualHash.Write(actual); err != nil {
			return false, err
		}
		return reflect.DeepEqual(expectedHash.Sum(nil), actualHash.Sum(nil)), nil
	}
}

// Compare funcs compare actual content to expected content, return the result whether they are equal.
// It should just be called by func Judge.
func compareAll(expected, actual io.Reader, buffLen int) (bool, error) {
	expectHash := sha256.New()
	actualHash := sha256.New()

	expectReader := bufio.NewReaderSize(expected, buffLen)
	actualReader := bufio.NewReaderSize(actual, buffLen)

	expectLen, err := expectReader.WriteTo(expectHash)
	if err != nil {
		return false, nil
	}

	actualLen, err := actualReader.WriteTo(actualHash)
	if err != nil {
		return false, nil
	}

	return (expectLen == actualLen) && bytes.Equal(expectHash.Sum(nil), actualHash.Sum(nil)), nil
}

func compareLines(expected, actual io.Reader, ignoreNewline bool, fn compareFunc) (bool, error) {
	//return compareLines2(expected, actual, ignoreNewline)
	expectedOutputScanner := bufio.NewScanner(expected)
	actualOutputReader := bufio.NewReader(actual)
	expectedOutputScanner.Scan()

	for {
		// expectedBuffScanner will ignore '\n'
		expectedOutputLine := expectedOutputScanner.Bytes()

		actualOutputLine, actualErr := actualOutputReader.ReadSlice('\n')
		if actualErr != nil && actualErr != io.EOF {
			return false, actualErr
		}
		if len(actualOutputLine) != 0 {
			actualOutputLine = filter(actualOutputLine)
		}

		if len(expectedOutputLine) != len(actualOutputLine) {
			return false, nil
		} else {
			ok, err := fn(expectedOutputLine, actualOutputLine)
			if err != nil {
				return false, err
			}
			if !ok {
				return false, nil
			}
		}

		if !expectedOutputScanner.Scan() {
			// finish reading expected
			if actualErr == io.EOF {
				// finish reading actualOutputReader
				return true, nil
			}

			// actualOutputReader has next line to be read
			if !ignoreNewline {
				return false, nil
			}

			actualOutputLine, actualErr = actualOutputReader.ReadSlice('\n')
			if len(actualOutputLine) == 0 && actualErr == io.EOF {
				return true, nil
			} else {
				return false, actualErr
			}
		}
	}
}

func iterHash(r *bufio.Reader, h hash.Hash, ignoreNewline bool) error {
	b, err := r.ReadBytes(newline)
	if err != nil && err != io.EOF {
		return err
	}

	var (
		hashErr error
		trim    = filter(b)
	)

	if _, hashErr = h.Write(trim); hashErr != nil {
		return hashErr
	}

	if ignoreNewline || len(b) == len(trim) || (len(b) > 0 && b[len(b)-1] != '\n') {
		return err
	}
	if _, hashErr = h.Write([]byte{newline}); hashErr != nil {
		return hashErr
	}
	return err
}

func compareLines2(expected, actual io.Reader, ignoreNewline bool) (bool, error) {

	var (
		expectHash   = md5.New()
		actualHash   = md5.New()
		expectReader = bufio.NewReader(expected)
		actualReader = bufio.NewReader(actual)
		finExpect    bool
		finActual    bool
	)

	// Synchronized comparison.
	for {
		if err := iterHash(expectReader, expectHash, ignoreNewline); err != nil {
			finExpect = true
			if err != io.EOF {
				return false, err
			}
			break
		}
		if err := iterHash(actualReader, actualHash, ignoreNewline); err != nil {
			finActual = true
			if err != io.EOF {
				return false, err
			}
			break
		}

		if !bytes.Equal(expectHash.Sum(nil), actualHash.Sum(nil)) {
			return false, nil
		}
	}

	for !finExpect {
		if err := iterHash(expectReader, expectHash, ignoreNewline); err != nil {
			if err != io.EOF {
				return false, err
			}
			break
		}
	}

	for !finActual {
		if err := iterHash(actualReader, actualHash, ignoreNewline); err != nil {
			if err != io.EOF {
				return false, err
			}
			break
		}
	}

	return bytes.Equal(expectHash.Sum(nil), actualHash.Sum(nil)), nil
}

func filter(data []byte) []byte {
	l := len(data)
	if l > 0 && data[l-1] == '\n' {
		l--
	}
	if l > 0 && data[l-1] == '\r' {
		l--
	}
	return data[:l]
}

func compareFloats(expected, actual io.Reader, precision int) (bool, error) {
	// 10 ** (-309) < float64 < 10 ** 309
	if precision > 308 {
		return false, errors.New("precision should not bigger than 308")
	}

	expectedFloat, err := readFloat64(expected)
	if err != nil {
		return false, fmt.Errorf("failed to compare float, err: %v", err)
	}

	actualFloat, err := readFloat64(actual)
	if err != nil {
		return false, fmt.Errorf("failed to compare float, err: %v", err)
	}

	if math.Abs(actualFloat-expectedFloat) >= math.Pow(10., -float64(precision)) {
		return false, nil
	}
	return true, nil
}

func readFloat64(r io.Reader) (float64, error) {
	var res float64
	buff := make([]byte, 512)
	if n, err := io.ReadFull(r, buff); err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return 0., err
	} else {
		if n != 0 {
			buff = filter(buff[:n])
		}

		res, err = strconv.ParseFloat(string(buff), 64)
		if err != nil {
			return 0., err
		}
	}
	return res, nil
}
