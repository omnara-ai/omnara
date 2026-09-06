package model

import "github.com/omnara-ai/omnara/internal/modelprotocol"

type ProviderRoute struct {
	APIFormat         modelprotocol.APIFormat
	APIVariant        modelprotocol.APIVariant
	BaseURL           string
	ProviderModelSlug string
}
