package anthropicmessages

import "github.com/omnara-ai/omnara/internal/model"

type CacheControl struct {
	Type string `json:"type"`
	TTL  string `json:"ttl,omitempty"`
}

func CacheControlFor(plan model.PromptCachePlan) *CacheControl {
	if !plan.Explicit {
		return nil
	}
	control := &CacheControl{Type: "ephemeral"}
	if plan.LongRetention {
		control.TTL = "1h"
	}
	return control
}
