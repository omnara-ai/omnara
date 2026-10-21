//go:build integration

package integrationdb

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func GeneratedQuery(t testing.TB, path, name string) string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	require.NoError(t, err)
	var query string
	ast.Inspect(file, func(node ast.Node) bool {
		literal, ok := node.(*ast.BasicLit)
		if !ok || literal.Kind != token.STRING {
			return true
		}
		text, err := strconv.Unquote(literal.Value)
		require.NoError(t, err)
		if strings.HasPrefix(text, "-- name: "+name+" ") {
			query = text
		}
		return true
	})
	require.NotEmpty(t, query, name)
	return query
}

type PlanNode struct {
	Relation string     `json:"Relation Name"`
	Index    string     `json:"Index Name"`
	Type     string     `json:"Node Type"`
	Rows     float64    `json:"Actual Rows"`
	Loops    float64    `json:"Actual Loops"`
	Hit      int        `json:"Shared Hit Blocks"`
	Read     int        `json:"Shared Read Blocks"`
	Plans    []PlanNode `json:"Plans"`
}

func ReadPlan(t testing.TB, raw []byte) PlanNode {
	t.Helper()
	var plans []struct{ Plan PlanNode }
	require.NoError(t, json.Unmarshal(raw, &plans))
	require.Len(t, plans, 1)
	return plans[0].Plan
}

func (p PlanNode) Walk(visit func(PlanNode)) {
	visit(p)
	for _, child := range p.Plans {
		child.Walk(visit)
	}
}

func PlanBuffers(t testing.TB, raw []byte) int {
	t.Helper()
	plan := ReadPlan(t, raw)
	return plan.Hit + plan.Read
}
