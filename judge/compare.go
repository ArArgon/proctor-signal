package judge

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
	"strconv"
)

// Compare funcs compare actual content to expected content, return the result whether they are equal.
// It should just be called by func Judge.

func compareAll(expected, actual io.Reader, buffLen int) (bool, error) {
	expectedOutputBuff := make([]byte, buffLen)
	actualOutputBuff := make([]byte, buffLen)
	var expectedLen, actualLen int
	var err error
	for {
		expectedLen, err = io.ReadFull(expected, expectedOutputBuff)
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			return false, err
		}

		actualLen, err = io.ReadFull(actual, actualOutputBuff)
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			return false, err
		}

		if len(expectedOutputBuff) != len(actualOutputBuff) ||
			!reflect.DeepEqual(expectedOutputBuff[:expectedLen], actualOutputBuff[:actualLen]) {
			return false, nil
		}

		if expectedLen < buffLen {
			break
		}
	}

	return true, nil
}

func filter(data []byte) []byte {
	l := len(data)
	if data[l-1] == '\n' {
		l--
	}
	if data[l-1] == '\r' {
		l--
	}
	// if data[l-1] == ' ' {
	// 	l--
	// }
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

func compareLines(expected, actual io.Reader, ignoreNewline bool) (bool, error) {
	expectedBuffReader := bufio.NewReader(expected)
	actualBuffReader := bufio.NewReader(actual)
	for {
		expectedOutputLine, expectedErr := expectedBuffReader.ReadSlice('\n')
		if expectedErr != nil && expectedErr != io.EOF && expectedErr != io.ErrUnexpectedEOF {
			return false, expectedErr
		}

		actualOutputLine, actualErr := actualBuffReader.ReadSlice('\n')
		if actualErr != nil && actualErr != io.EOF && actualErr != io.ErrUnexpectedEOF {
			return false, actualErr
		}

		if expectedErr == io.EOF && actualErr == io.EOF &&
			len(expectedOutputLine) == 0 && len(actualOutputLine) == 0 {
			// both expectedBuffReader and actualBuffReader have '\n' at the end, and both next line is empty
			if ignoreNewline {
				return true, nil
			} else {
				return false, nil
			}
		}

		if ignoreNewline && expectedErr == io.EOF && actualErr != io.EOF {
			// expectedBuffReader do not have '\n' at the end, but actualBuffReader do
			buff, actualErr := actualBuffReader.ReadSlice('\n')
			if actualErr != nil && actualErr != io.EOF && actualErr != io.ErrUnexpectedEOF {
				return false, actualErr
			}

			if len(buff) == 0 && actualErr == io.EOF {
				// actualBuffReader's next line is empty
				actualOutputLine = filter(actualOutputLine)
			} else {
				return false, nil
			}
		} else if actualErr == io.EOF && expectedErr != io.EOF {
			// actualBuffReader do not have '\n' at the end, but expectedBuffReader do
			buff, actualErr := expectedBuffReader.ReadSlice('\n')
			if actualErr != nil && actualErr != io.EOF && actualErr != io.ErrUnexpectedEOF {
				return false, actualErr
			}

			if len(buff) == 0 && actualErr == io.EOF {
				// expectedBuffReader's next line is empty
				expectedOutputLine = filter(expectedOutputLine)
			} else {
				return false, nil
			}
		}

		if len(expectedOutputLine) != len(actualOutputLine) ||
			!reflect.DeepEqual(expectedOutputLine, actualOutputLine) {
			return false, nil
		}

		if expectedErr == io.EOF || actualErr == io.EOF {
			break
		}
	}
	return true, nil
}

func compareHash(expected, actual io.Reader, buffLen int, hashFunc func(data []byte) []byte) (bool, error) {
	expectedOutputBuff := make([]byte, buffLen)
	actualOutputBuff := make([]byte, buffLen)
	var expectedLen, actualLen int
	var err error
	for {
		expectedLen, err = io.ReadFull(expected, expectedOutputBuff)
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			return false, err
		}

		actualLen, err = io.ReadFull(actual, actualOutputBuff)
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			return false, err
		}

		if expectedLen < buffLen {
			// finish reading expected, ignore SPACE or ENTER at the end of buff
			if expectedLen != 0 {
				expectedOutputBuff = filter(expectedOutputBuff[:expectedLen])
			}
			if actualLen != 0 {
				actualOutputBuff = filter(actualOutputBuff[:actualLen])
			}
		}

		if len(expectedOutputBuff) != len(actualOutputBuff) ||
			!reflect.DeepEqual(hashFunc(expectedOutputBuff), hashFunc(actualOutputBuff)) {
			return false, nil
		}

		if expectedLen < buffLen {
			break
		}
	}
	return true, nil
}
