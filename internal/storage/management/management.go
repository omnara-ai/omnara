package management

import (
	"fmt"

	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type Kind string

const (
	Tenant  Kind = "tenant"
	Cluster Kind = "cluster"
)

func Validate(kind Kind) error {
	switch kind {
	case Tenant, Cluster:
		return nil
	default:
		return fmt.Errorf("management kind %q is invalid", kind)
	}
}

func RequireTenant(kind Kind, resource string) error {
	if kind == Tenant {
		return nil
	}
	return fmt.Errorf("cluster-managed %s cannot be changed: %w", resource, storeerr.ErrStateTransitionConflict)
}
