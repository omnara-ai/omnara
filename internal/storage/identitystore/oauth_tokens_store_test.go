package identitystore

import (
	"strings"
	"testing"
)

func TestValidateOAuthURLField(t *testing.T) {
	for name, tc := range map[string]struct {
		value   string
		wantErr bool
	}{
		"https url":       {value: "https://client.example/c.json?x=1"},
		"invalid utf8":    {value: "https://client.example/c.json?x=\xff", wantErr: true},
		"nul byte":        {value: "https://client.example/c.json?x=\x00", wantErr: true},
		"delete control":  {value: "https://client.example/c.json?x=\x7f", wantErr: true},
		"too long":        {value: "https://client.example/" + strings.Repeat("a", oauthURLFieldMaxBytes), wantErr: true},
		"non ascii query": {value: "https://client.example/c.json?x=é"},
	} {
		t.Run(name, func(t *testing.T) {
			err := validateOAuthURLField("client_id", tc.value)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateOAuthURLField(%q) error = %v, wantErr %t", tc.value, err, tc.wantErr)
			}
		})
	}
}
