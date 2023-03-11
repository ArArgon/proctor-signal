package judge

import (
	"bufio"
	"crypto/md5"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
	"strconv"
)

var hashFuncs = map[string]func(data []byte) []byte{
	"noHash": func(data []byte) []byte {
		return data
	},
	"md5": func(data []byte) []byte {
		res := md5.Sum(data)
		return res[:]
	},
}

// Compare funcs compare actual content to expected content, return the result whether they are equal.
// It should just be called by func Judge.

func compareAll(expected, actual io.Reader, buffLen int, hashFunc func(data []byte) []byte) (bool, error) {
	expectedOutputBuff := make([]byte, buffLen)
	actualOutputBuff := make([]byte, buffLen)
	var expectedLen, actualLen int
	var err error
	if hashFunc == nil {
		hashFunc = hashFuncs["noHash"]
	}

	for {
		expectedLen, err = io.ReadFull(expected, expectedOutputBuff)
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			return false, err
		}

		actualLen, err = io.ReadFull(actual, actualOutputBuff)
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			return false, err
		}

		if expectedLen != actualLen ||
			!reflect.DeepEqual(hashFunc(expectedOutputBuff[:expectedLen]), hashFunc(actualOutputBuff[:actualLen])) {
			return false, nil
		}

		if expectedLen < buffLen {
			break
		}
	}
	return true, nil
}

func compareLines(expected, actual io.Reader, ignoreNewline bool, hashFunc func(data []byte) []byte) (bool, error) {
	expectedOutputScanner := bufio.NewScanner(expected)
	actualOutputReader := bufio.NewReader(actual)
	expectedOutputScanner.Scan()
	if hashFunc == nil {
		hashFunc = hashFuncs["noHash"]
	}

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

		if len(expectedOutputLine) != len(actualOutputLine) ||
			!reflect.DeepEqual(hashFunc(expectedOutputLine), hashFunc(actualOutputLine)) {
			return false, nil
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

func filter(data []byte) []byte {
	l := len(data)
	if data[l-1] == '\n' {
		l--
	}
	if data[l-1] == '\r' {
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
