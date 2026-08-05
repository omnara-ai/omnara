package integrationredis

import "testing"

func TestValidateTestRedisURL(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantErr bool
	}{
		{name: "localhost", raw: "redis://localhost:6379/0"},
		{name: "loopback", raw: "redis://127.0.0.1:6379/0"},
		{name: "alternate local port", raw: "redis://127.0.0.1:6380/0"},
		{name: "wrong host", raw: "redis://redis.example.com:6379/0", wantErr: true},
		{name: "wrong scheme", raw: "rediss://127.0.0.1:6379/0", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateTestRedisURL(tt.raw)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateTestRedisURL(%q) error = %v, wantErr %t", tt.raw, err, tt.wantErr)
			}
		})
	}
}
