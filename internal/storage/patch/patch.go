package patch

import "github.com/omnara-ai/omnara/internal/modelprice"

type NullableInt struct {
	Set   bool
	Value *int
}

type NullableBool struct {
	Set   bool
	Value *bool
}

type NullablePricing struct {
	Set   bool
	Value *modelprice.Pricing
}
