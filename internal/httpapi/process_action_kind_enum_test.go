package httpapi

import (
	"testing"

	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/processaction"
)

func TestReconciliationEnumsAcceptEveryProcessActionKind(t *testing.T) {
	t.Parallel()
	for _, kind := range []processaction.Kind{
		processaction.KindWrite,
		processaction.KindRead,
		processaction.KindInterrupt,
		processaction.KindTerminate,
		processaction.KindWatch,
	} {
		if !kind.Valid() {
			t.Fatalf("%s is not a valid process action kind", kind)
		}
		if !openapi.ProcessActionReconciliationDirectiveActionKind(kind).Valid() {
			t.Fatalf("reconciliation directives reject %s", kind)
		}
		if kind.Mutating() && !openapi.ProcessActionReconciliationClaimActionKind(kind).Valid() {
			t.Fatalf("daemon reconciliation claims reject persisted %s actions", kind)
		}
	}
}
