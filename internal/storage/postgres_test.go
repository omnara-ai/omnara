package storage

import (
	"testing"
	"time"
)

const testDatabaseURL = "postgres://user:password@database.invalid/omnara"

func TestParsePoolConfigUsesOmnaraDefaultsWhenOmitted(t *testing.T) {
	cfg, err := parsePoolConfig(testDatabaseURL)
	if err != nil {
		t.Fatalf("parse pool config: %v", err)
	}
	if cfg.MaxConns != 10 {
		t.Fatalf("max conns = %d, want 10", cfg.MaxConns)
	}
	if cfg.MinConns != 1 {
		t.Fatalf("min conns = %d, want 1", cfg.MinConns)
	}
	if cfg.MaxConnLifetime != 30*time.Minute {
		t.Fatalf("max connection lifetime = %s, want 30m", cfg.MaxConnLifetime)
	}
	if cfg.MaxConnLifetimeJitter != 5*time.Minute {
		t.Fatalf("max connection lifetime jitter = %s, want 5m", cfg.MaxConnLifetimeJitter)
	}
}

func TestParsePoolConfigPreservesExplicitSettings(t *testing.T) {
	for _, test := range []struct {
		name        string
		databaseURL string
	}{
		{
			name: "URL query",
			databaseURL: testDatabaseURL +
				"?pool_max_conns=23&pool_min_conns=4&pool_max_conn_lifetime=47m" +
				"&pool_max_conn_lifetime_jitter=6m",
		},
		{
			name: "keyword DSN",
			databaseURL: "host=database.invalid user=user password=password dbname=omnara " +
				"pool_max_conns=23 pool_min_conns=4 pool_max_conn_lifetime=47m " +
				"pool_max_conn_lifetime_jitter=6m",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg, err := parsePoolConfig(test.databaseURL)
			if err != nil {
				t.Fatalf("parse pool config: %v", err)
			}
			if cfg.MaxConns != 23 {
				t.Fatalf("max conns = %d, want 23", cfg.MaxConns)
			}
			if cfg.MinConns != 4 {
				t.Fatalf("min conns = %d, want 4", cfg.MinConns)
			}
			if cfg.MaxConnLifetime != 47*time.Minute {
				t.Fatalf("max connection lifetime = %s, want 47m", cfg.MaxConnLifetime)
			}
			if cfg.MaxConnLifetimeJitter != 6*time.Minute {
				t.Fatalf("max connection lifetime jitter = %s, want 6m", cfg.MaxConnLifetimeJitter)
			}
		})
	}
}

func TestDefaultApplicationNameHonorsExplicitConfiguration(t *testing.T) {
	for _, test := range []struct {
		name        string
		databaseURL string
		want        string
	}{
		{name: "omitted", databaseURL: testDatabaseURL, want: "omnara-test"},
		{
			name:        "explicit URL setting",
			databaseURL: testDatabaseURL + "?application_name=operator-name",
			want:        "operator-name",
		},
		{
			name: "explicit keyword DSN setting",
			databaseURL: "host=database.invalid user=user password=password dbname=omnara " +
				"application_name=keyword-name",
			want: "keyword-name",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg, err := parsePoolConfig(test.databaseURL)
			if err != nil {
				t.Fatalf("parse pool config: %v", err)
			}
			WithDefaultApplicationName("omnara-test")(cfg)
			if got := cfg.ConnConfig.Config.RuntimeParams["application_name"]; got != test.want {
				t.Fatalf("application name = %q, want %q", got, test.want)
			}
		})
	}
}
