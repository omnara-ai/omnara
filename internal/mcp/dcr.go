package mcp

import (
	"regexp"
	"strconv"
	"strings"
)

var clientRegistrationStatusPattern = regexp.MustCompile(`^registration failed with status (\d{3})[^:]*: (.*)$`)

func ClientRegistrationFailure(err error) error {
	match := clientRegistrationStatusPattern.FindStringSubmatch(err.Error())
	if match == nil {
		return err
	}
	status, convErr := strconv.Atoi(match[1])
	if convErr != nil {
		return err
	}
	return &HTTPError{Status: status, Body: []byte(strings.TrimSpace(match[2]))}
}
