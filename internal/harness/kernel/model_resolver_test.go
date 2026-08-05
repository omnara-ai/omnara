//go:build integration

package kernel

import (
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/storage"
	modelresolvertest "github.com/omnara-ai/omnara/internal/testutil/modelresolver"
)

func liveTestModelResolver(store *storage.Store, client model.Client) model.Resolver {
	return modelresolvertest.LiveGrant{Store: store, Client: client}
}

type staticTestModelResolver = modelresolvertest.Static
