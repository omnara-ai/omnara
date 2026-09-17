//go:build integration

package tools

import (
	"fmt"
	"os"
	"testing"

	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == SearchProcessCommand {
		if err := RunSearchProcess(os.Args[2:]); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	integrationdb.RunTestMain(m)
}
