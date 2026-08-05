package integrationdb

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

const generatedDatabasePrefix = "omnara_test_"

func validateTestDatabaseURL(databaseURL string) error {
	parsed, err := url.Parse(databaseURL)
	if err != nil {
		return fmt.Errorf("parse local test database URL: %w", err)
	}
	user := ""
	if parsed.User != nil {
		user = parsed.User.Username()
	}
	host := parsed.Hostname()
	port := parsed.Port()
	database := strings.TrimPrefix(parsed.Path, "/")
	localHost := host == "127.0.0.1" || host == "localhost"
	if _, err := strconv.Atoi(port); err != nil || port == "5432" {
		return fmt.Errorf(
			"refusing local test database URL without an explicit non-default port: %s",
			redactDatabaseURL(databaseURL),
		)
	}
	if parsed.Scheme != "postgres" || !localHost || database != "omnara" || user != "omnara" {
		return fmt.Errorf("refusing non-local test database URL: %s", redactDatabaseURL(databaseURL))
	}
	return nil
}

func AssertTestDatabaseURL(t testing.TB, databaseURL string) {
	t.Helper()
	if err := validateTestDatabaseURL(databaseURL); err != nil {
		t.Fatal(err)
	}
}

func redactDatabaseURL(databaseURL string) string {
	if at := strings.LastIndex(databaseURL, "@"); at >= 0 {
		return "postgres://<redacted>" + databaseURL[at:]
	}
	return databaseURL
}
