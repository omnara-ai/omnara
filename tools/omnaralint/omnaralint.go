package omnaralint

import (
	"go/ast"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/golangci/plugin-module-register/register"
	"golang.org/x/tools/go/analysis"
)

func init() {
	register.Plugin("omnaralint", New)
}

type Plugin struct{}

func New(settings any) (register.LinterPlugin, error) {
	return &Plugin{}, nil
}

func (p *Plugin) BuildAnalyzers() ([]*analysis.Analyzer, error) {
	return []*analysis.Analyzer{Analyzer}, nil
}

func (p *Plugin) GetLoadMode() string {
	return register.LoadModeTypesInfo
}

var Analyzer = &analysis.Analyzer{
	Name: "omnaralint",
	Doc:  "checks Omnara repository-specific invariants",
	Run:  run,
}

const (
	repoPackage             = "github.com/omnara-ai/omnara"
	internalPackage         = repoPackage + "/internal"
	storagePackage          = internalPackage + "/storage"
	dbsqlcImport            = storagePackage + "/internal/dbsqlc"
	testutilPackage         = internalPackage + "/testutil"
	machineDaemonPackage    = internalPackage + "/machinedaemon"
	stateDBPackage          = machineDaemonPackage + "/statedb"
	stateDBSQLCImport       = stateDBPackage + "/internal/dbsqlc"
	omnaradPackage          = internalPackage + "/omnarad"
	daemonEntrypointPackage = repoPackage + "/cmd/daemon"
)

var (
	stateSQLWildcardRE = regexp.MustCompile(`(?i)\b(select|returning)[[:space:]]+([a-z_][a-z0-9_]*\.)*\*`)
	stateSQLScanOnce   sync.Once
	stateSQLIssues     []sqlIssue
	stateSQLReportMu   sync.Mutex
	stateSQLReported   bool
)

type sqlIssue struct {
	path   string
	offset int
}

func run(pass *analysis.Pass) (any, error) {
	for _, file := range pass.Files {
		checkGoFile(pass, file)
	}
	reportStateSQLWildcardIssues(pass)

	return nil, nil //nolint:nilnil // An analyzer without ResultType must return a nil result.
}

func checkGoFile(pass *analysis.Pass, file *ast.File) {
	filename := slashPath(pass.Fset.Position(file.Pos()).Filename)
	packagePath := pass.Pkg.Path()
	inStorage := packageWithin(packagePath, storagePackage)
	inGeneratedDBSQLC := packageWithin(packagePath, dbsqlcImport) || packageWithin(packagePath, stateDBSQLCImport)
	inInternal := packageWithin(packagePath, internalPackage)
	inTestutil := packageWithin(packagePath, testutilPackage)
	isTest := strings.HasSuffix(filename, "_test.go")

	for _, imp := range file.Imports {
		importPath := strings.Trim(imp.Path.Value, `"`)
		if importPath == dbsqlcImport && !inStorage {
			pass.Reportf(imp.Pos(), "internal/storage/internal/dbsqlc imports must stay inside internal/storage")
		}
		if forbiddenStateDBImport(packagePath, importPath) {
			pass.Reportf(
				imp.Pos(),
				"daemon SQLite access must stay inside internal/machinedaemon/statedb",
			)
		}
	}
	ast.Inspect(file, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.BinaryExpr:
			if n.Op == token.SHL || n.Op == token.SHR {
				pass.Reportf(n.OpPos, "bit shift operators are not allowed; write the value or arithmetic explicitly")
			}
		case *ast.AssignStmt:
			if n.Tok == token.SHL_ASSIGN || n.Tok == token.SHR_ASSIGN {
				pass.Reportf(n.TokPos, "bit shift assignment operators are not allowed; write the arithmetic explicitly")
			}
		case *ast.InterfaceType:
			if inGeneratedDBSQLC && !isAllowedDBSQLCInterface(pass, n) {
				pass.Reportf(n.Pos(), "unexpected interface{} in generated dbsqlc; add an explicit SQL cast at the query boundary")
			}
		case *ast.SelectorExpr:
			if inGeneratedDBSQLC {
				if ident, ok := n.X.(*ast.Ident); ok && ident.Name == "pgtype" {
					pass.Reportf(
						n.Pos(),
						"unexpected pgtype usage in generated dbsqlc; prefer native sqlc overrides and explicit SQL casts",
					)
				}
			}
		case *ast.Ident:
			object := pass.TypesInfo.Uses[n]
			if inInternal && !isTest && !inGeneratedDBSQLC && isPackageFunction(object, "time", "Sleep") {
				pass.Reportf(
					n.Pos(),
					"production polling waits must be context-aware; use timers/select on ctx.Done instead of raw time.Sleep",
				)
			}
			if inStorage && !isTest && !inGeneratedDBSQLC && isPackageFunction(object, "time", "Now") {
				pass.Reportf(
					n.Pos(),
					"storage must use database-owned durable time; time.Now is not allowed in production storage code",
				)
			}
		case *ast.TypeSpec:
			if inStorage && !isTest && !inGeneratedDBSQLC && strings.HasSuffix(n.Name.Name, "Input") {
				checkDirectStorageInputFields(pass, n)
			}
		case *ast.FuncDecl:
			if inStorage && !isTest && !inGeneratedDBSQLC {
				checkExportedStorageMutationMethodTimeParams(pass, n)
			}
		case *ast.CallExpr:
			if inInternal && !isTest && !inGeneratedDBSQLC && !inTestutil {
				if ident, ok := n.Fun.(*ast.Ident); ok && ident.Name == "panic" {
					pass.Reportf(
						n.Pos(),
						"production panic calls are not allowed; return an error, model an explicit terminal state, or keep the panic in test-only code",
					)
				}
			}
		}

		return true
	})
}

func checkExportedStorageMutationMethodTimeParams(pass *analysis.Pass, declaration *ast.FuncDecl) {
	if declaration.Recv == nil || !declaration.Name.IsExported() {
		return
	}
	function, ok := pass.TypesInfo.Defs[declaration.Name].(*types.Func)
	if !ok {
		return
	}
	signature, ok := function.Type().(*types.Signature)
	if !ok || !isStorageMutationReceiver(signature.Recv()) {
		return
	}
	for index := range signature.Params().Len() {
		parameter := signature.Params().At(index)
		if !isTimeType(parameter.Type()) {
			continue
		}
		pass.Reportf(
			declaration.Name.Pos(),
			"exported storage mutation methods must not accept direct time.Time parameters; use a checked Input, a semantic domain value, or a policy duration",
		)
	}
}

func isStorageMutationReceiver(receiver *types.Var) bool {
	if receiver == nil {
		return false
	}
	receiverType := types.Unalias(receiver.Type())
	if pointer, ok := receiverType.(*types.Pointer); ok {
		receiverType = types.Unalias(pointer.Elem())
	}
	named, ok := receiverType.(*types.Named)
	if !ok {
		return false
	}
	object := named.Obj()
	if object.Pkg() == nil || !packageWithin(object.Pkg().Path(), storagePackage) {
		return false
	}
	return object.Name() == "Store" || object.Name() == "Service"
}

func packageWithin(packagePath, root string) bool {
	return packagePath == root || strings.HasPrefix(packagePath, root+"/")
}

func isPackageFunction(object types.Object, packagePath, name string) bool {
	function, ok := object.(*types.Func)
	return ok && function.Name() == name && function.Pkg() != nil && function.Pkg().Path() == packagePath
}

func checkDirectStorageInputFields(pass *analysis.Pass, spec *ast.TypeSpec) {
	structure, ok := spec.Type.(*ast.StructType)
	if !ok {
		pass.Reportf(
			spec.Type.Pos(),
			"storage Input types must declare an explicit struct so their accepted fields remain reviewable",
		)
		return
	}
	for _, field := range structure.Fields.List {
		if !isTimeType(pass.TypesInfo.TypeOf(field.Type)) {
			continue
		}
		if len(field.Names) == 0 {
			pass.Reportf(
				field.Pos(),
				"direct time.Time fields on storage Input types must be explicit Source* observations; use a semantic value type for domain timestamps, or pass a duration and let PostgreSQL derive durable time",
			)
			continue
		}
		for _, name := range field.Names {
			if strings.HasPrefix(name.Name, "Source") {
				continue
			}
			pass.Reportf(
				name.Pos(),
				"direct time.Time fields on storage Input types must be explicit Source* observations; use a semantic value type for domain timestamps, or pass a duration and let PostgreSQL derive durable time",
			)
		}
	}
}

func isTimeType(fieldType types.Type) bool {
	fieldType = types.Unalias(fieldType)
	if pointer, ok := fieldType.(*types.Pointer); ok {
		fieldType = types.Unalias(pointer.Elem())
	}

	named, ok := fieldType.(*types.Named)
	if !ok {
		return false
	}
	object := named.Obj()
	return object.Name() == "Time" && object.Pkg() != nil && object.Pkg().Path() == "time"
}

func isAllowedDBSQLCInterface(pass *analysis.Pass, interfaceType *ast.InterfaceType) bool {
	if len(interfaceType.Methods.List) != 0 {
		return true
	}

	allowedMethodNames := map[string]bool{
		"Exec":            true,
		"ExecContext":     true,
		"Query":           true,
		"QueryContext":    true,
		"QueryRow":        true,
		"QueryRowContext": true,
	}
	for _, file := range pass.Files {
		for _, decl := range file.Decls {
			genDecl, ok := decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, spec := range genDecl.Specs {
				typeSpec, ok := spec.(*ast.TypeSpec)
				if !ok || typeSpec.Name.Name != "DBTX" {
					continue
				}
				dbtx, ok := typeSpec.Type.(*ast.InterfaceType)
				if !ok || dbtx.Methods == nil {
					continue
				}
				for _, method := range dbtx.Methods.List {
					if len(method.Names) != 1 ||
						!allowedMethodNames[method.Names[0].Name] {
						continue
					}
					funcType, ok := method.Type.(*ast.FuncType)
					if !ok || funcType.Params == nil ||
						len(funcType.Params.List) == 0 {
						continue
					}
					last := funcType.Params.List[len(funcType.Params.List)-1]
					ellipsis, ok := last.Type.(*ast.Ellipsis)
					if ok && ellipsis.Elt == interfaceType {
						return true
					}
				}
			}
		}
	}

	return false
}

func forbiddenStateDBImport(packagePath, importPath string) bool {
	if packageWithin(packagePath, stateDBPackage) {
		return false
	}
	inDaemon := packageWithin(packagePath, daemonEntrypointPackage) ||
		packageWithin(packagePath, machineDaemonPackage) ||
		packageWithin(packagePath, omnaradPackage)
	if !inDaemon {
		return false
	}
	return importPath == "database/sql" ||
		strings.HasPrefix(importPath, "database/sql/") ||
		importPath == "modernc.org/sqlite" ||
		strings.HasPrefix(importPath, "modernc.org/sqlite/")
}

func reportStateSQLWildcardIssues(pass *analysis.Pass) {
	stateSQLScanOnce.Do(func() {
		stateSQLIssues = scanStateSQLWildcardIssues("internal/machinedaemon/statedb/queries")
	})

	stateSQLReportMu.Lock()
	if stateSQLReported {
		stateSQLReportMu.Unlock()
		return
	}
	stateSQLReported = true
	stateSQLReportMu.Unlock()

	for _, issue := range stateSQLIssues {
		content, err := os.ReadFile(issue.path)
		if err != nil {
			continue
		}

		file := pass.Fset.AddFile(issue.path, -1, len(content))
		file.SetLinesForContent(content)
		pass.Report(analysis.Diagnostic{
			Pos:      file.Pos(issue.offset),
			Category: "storage-sql",
			Message:  "storage queries must return explicit columns; wildcard outputs hide schema-contract changes",
		})
	}
}

func scanStateSQLWildcardIssues(dir string) []sqlIssue {
	var issues []sqlIssue
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || filepath.Ext(path) != ".sql" {
			return err
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, match := range stateSQLWildcardRE.FindAllIndex(content, -1) {
			issues = append(issues, sqlIssue{path: slashPath(path), offset: match[0]})
		}
		return nil
	})
	if err != nil {
		return issues
	}

	return issues
}

func slashPath(path string) string {
	path = filepath.ToSlash(path)
	wd, err := os.Getwd()
	if err != nil {
		return path
	}

	wd = filepath.ToSlash(wd)
	path = strings.TrimPrefix(path, wd+"/")
	return strings.TrimPrefix(path, "./")
}
