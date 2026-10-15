package omnaralint

import (
	"encoding/json"
	"go/ast"
	"go/constant"
	"go/types"
	"strings"

	pgquery "github.com/wasilibs/go-pgquery"
	"golang.org/x/tools/go/analysis"
)

const executionPackage = storagePackage + "/internal/agentexecution"

func executionRelation(name string) bool {
	switch name {
	case "agents",
		"agent_inputs",
		"agent_events",
		"agent_turns",
		"model_call_contexts",
		"model_outputs",
		"tool_calls",
		"tool_call_results",
		"context_checkpoints",
		"content_blocks",
		"agent_interactions",
		"agent_execution_state",
		"agent_wakeups",
		"agent_runtime_locks":
		return true
	}
	return false
}

func executionSQL(sql string) (bool, error) {
	parsed, err := pgquery.ParseToJSON(sql)
	if err != nil {
		return false, err
	}
	var tree any
	if err = json.Unmarshal([]byte(parsed), &tree); err != nil {
		return false, err
	}
	var visit func(any) bool
	visit = func(value any) bool {
		switch node := value.(type) {
		case []any:
			for _, child := range node {
				if visit(child) {
					return true
				}
			}
		case map[string]any:
			for kind, child := range node {
				switch kind {
				case "InsertStmt", "UpdateStmt", "DeleteStmt", "MergeStmt", "CopyStmt":
					statement, _ := child.(map[string]any)
					relation, _ := statement["relation"].(map[string]any)
					name, _ := relation["relname"].(string)
					if executionRelation(name) {
						return true
					}
				case "DoStmt", "CallStmt", "TruncateStmt", "CreateFunctionStmt", "AlterTableStmt", "DropStmt":
					return true
				}
				if visit(child) {
					return true
				}
			}
		}
		return false
	}
	return visit(tree), nil
}

func checkExecutionAccess(pass *analysis.Pass, file *ast.File, isTest bool) {
	if isTest || packageWithin(pass.Pkg.Path(), executionPackage) ||
		packageWithin(pass.Pkg.Path(), dbsqlcImport) ||
		packageWithin(pass.Pkg.Path(), testutilPackage) ||
		packageWithin(pass.Pkg.Path(), repoPackage+"/migrations") {
		return
	}
	facade := packageWithin(pass.Pkg.Path(), storagePackage)
	queryForwarder := pass.Pkg.Path() == storagePackage+"/internal/storeutil" &&
		strings.HasSuffix(pass.Fset.Position(file.Pos()).Filename, "/pool.go")
	for _, imp := range file.Imports {
		if packageWithin(strings.Trim(imp.Path.Value, `"`), "github.com/omnara-ai/omnara/internal/testutil") {
			pass.Reportf(imp.Pos(), "production code cannot import execution test fixtures")
		}
		if strings.Trim(imp.Path.Value, `"`) == executionPackage+"/internal/executiondb" {
			pass.Reportf(imp.Pos(), "executiondb imports belong to agentexecution")
		}
	}
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		if selector, ok := call.Fun.(*ast.SelectorExpr); ok &&
			(selector.Sel.Name == "CopyFrom" || selector.Sel.Name == "SendBatch") &&
			facade {
			pass.Reportf(call.Pos(), "execution-capable bulk SQL belongs to agentexecution")
			return true
		}
		signature, ok := pass.TypesInfo.TypeOf(call.Fun).(*types.Signature)
		if ok && facade &&
			(strings.Contains(types.TypeString(signature,
				nil),
				"github.com/jackc/pgx/v5.Batch") ||
				strings.Contains(types.TypeString(signature,
					nil),
					"github.com/jackc/pgx/v5.CopyFromSource")) {
			pass.Reportf(call.Pos(), "execution-capable bulk SQL belongs to agentexecution")
			return true
		}
		if !ok || signature.Params().Len() < 2 || signature.Results().Len() == 0 || len(call.Args) < 2 {
			return true
		}
		if types.TypeString(signature.Params().At(0).Type(), nil) != "context.Context" ||
			types.TypeString(signature.Params().At(1).Type(), nil) != "string" {
			return true
		}
		result := types.TypeString(signature.Results().At(0).Type(), nil)
		if !strings.Contains(result, "github.com/jackc/pgx/") && !strings.Contains(result, "database/sql.") {
			return true
		}
		value := pass.TypesInfo.Types[call.Args[1]].Value
		if value == nil || value.Kind() != constant.String {
			if facade && !queryForwarder {
				pass.Reportf(call.Pos(), "dynamic SQL cannot bypass the execution DML boundary")
			}
			return true
		}
		writes, err := executionSQL(constant.StringVal(value))
		if writes {
			pass.Reportf(call.Pos(), "execution writes belong to agentexecution semantic commands")
		}
		if err != nil && facade {
			pass.Reportf(call.Pos(), "SQL must be statically parseable outside agentexecution")
		}
		return true
	})
}
