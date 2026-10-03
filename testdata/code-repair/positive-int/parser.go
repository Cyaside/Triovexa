package positiveint

import (
	"errors"
	"strconv"
)

// ParsePositive parses a positive integer supplied by a job producer.
func ParsePositive(raw string) (int, error) {
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, err
	}
	if value <= 1 {
		return 0, errors.New("value must be positive")
	}
	return value, nil
}
