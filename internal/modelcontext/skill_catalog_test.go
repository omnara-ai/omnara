package modelcontext

import (
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/storage/skillstore"
)

func TestSkillCatalogBlockKeepsUserContentVerbatim(t *testing.T) {
	got := skillCatalogBlock([]skillstore.SkillRecord{{
		Name:        "fix-build",
		Description: `Use when the user's code doesn't compile & "make" fails <loudly>`,
	}})
	for _, want := range []string{
		"<available_skills>",
		"    <name>fix-build</name>\n",
		"    <description>Use when the user's code doesn't compile & \"make\" fails <loudly></description>\n",
		"</available_skills>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("catalog block missing expected substring %q\nfull block:\n%s", want, got)
		}
	}
}

func TestSkillCatalogBlockPathHintDoesNotDoubleOmnaraSegment(t *testing.T) {
	got := skillCatalogBlock([]skillstore.SkillRecord{{
		Name:        "harmless",
		Description: "harmless",
	}})
	if strings.Contains(got, "$OMNARA_HOME/omnara/installations") {
		t.Fatalf("catalog still surfaces the bogus $OMNARA_HOME/omnara/installations path hint:\n%s", got)
	}
	want := "$OMNARA_HOME/installations/*/machines/*/skills/{skill_public_id}/revisions/{skill_revision_public_id}/"
	if !strings.Contains(got, want) {
		t.Fatalf("catalog is missing the expected %s hint:\n%s", want, got)
	}
}
