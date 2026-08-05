package main

import (
	"io/fs"
	"os"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/config"
)

func TestWebAssetsDefaultsToDisabled(t *testing.T) {
	assets, ok, err := webAssets(config.Config{})
	if err != nil {
		t.Fatalf("default web assets: %v", err)
	}
	if ok || assets != nil {
		t.Fatalf("default web assets should be disabled, got ok=%t assets=%v", ok, assets)
	}
}

func TestWebAssetsEmbeddedBuild(t *testing.T) {
	assets, ok, err := webAssets(config.Config{WebServing: config.WebServingEmbedded})
	if err == nil {
		if !ok {
			t.Fatal("embedded web assets should be enabled when bundled index.html exists")
		}
		if assets == nil {
			t.Fatal("embedded web assets must not be nil when bundled index.html exists")
		}
		if _, err := fs.Stat(assets, "index.html"); err != nil {
			t.Fatalf("embedded dist assets should include index.html, got error: %v", err)
		}
		return
	}
	if !strings.Contains(err.Error(), "embedded web assets") || !strings.Contains(err.Error(), "index.html") {
		t.Fatalf("embedded web assets error = %v, want embedded index.html context", err)
	}
	if ok || assets != nil {
		t.Fatalf("embedded web assets without index.html should be disabled, got ok=%t assets=%v", ok, assets)
	}
}

func TestWebAssetsDisabled(t *testing.T) {
	assets, ok, err := webAssets(config.Config{WebServing: config.WebServingDisabled})
	if err != nil {
		t.Fatalf("disabled web assets: %v", err)
	}
	if ok {
		t.Fatal("disabled web assets should report disabled")
	}
	if assets != nil {
		t.Fatal("disabled web assets must be nil")
	}
}

func TestRequireWebIndexRejectsMissingIndex(t *testing.T) {
	err := requireWebIndex(os.DirFS(t.TempDir()), "test web assets")
	if err == nil {
		t.Fatal("expected missing index.html to fail")
	}
	if !strings.Contains(err.Error(), "test web assets") || !strings.Contains(err.Error(), "index.html") {
		t.Fatalf("missing index error = %v, want source/index context", err)
	}
}
