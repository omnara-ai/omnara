package daemonversion

import (
	"fmt"
	"strconv"
	"strings"
)

const Development = "0.0.0-dev"

type Release [3]uint64

func ParseRelease(raw string) (Release, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return Release{}, fmt.Errorf("version %q must be MAJOR.MINOR.PATCH", raw)
	}
	var parsed Release
	for i, part := range parts {
		if part == "" || len(part) > 1 && part[0] == '0' {
			return Release{}, fmt.Errorf("version %q must be MAJOR.MINOR.PATCH", raw)
		}
		for _, r := range part {
			if r < '0' || r > '9' {
				return Release{}, fmt.Errorf("version %q must be MAJOR.MINOR.PATCH", raw)
			}
		}
		value, err := strconv.ParseUint(part, 10, 64)
		if err != nil {
			return Release{}, fmt.Errorf("version %q must be MAJOR.MINOR.PATCH", raw)
		}
		parsed[i] = value
	}
	return parsed, nil
}

func Compare(left, right Release) int {
	for i := range left {
		if left[i] < right[i] {
			return -1
		}
		if left[i] > right[i] {
			return 1
		}
	}
	return 0
}

func Validate(raw string) error {
	if raw == Development {
		return nil
	}
	_, err := ParseRelease(raw)
	return err
}
