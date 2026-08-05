package main

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	pg_query "github.com/pganalyze/pg_query_go/v6"
	pgquery "github.com/wasilibs/go-pgquery"
	pgparser "github.com/wasilibs/go-pgquery/parser"
	"google.golang.org/protobuf/reflect/protoreflect"
)

const (
	ruleExplicitOutputColumns  = "explicit-output-columns"
	ruleExplicitDurableTime    = "explicit-durable-current-time"
	ruleNoApplicationNow       = "no-application-now-parameter"
	ruleDatabaseOwnedTime      = "database-owned-time"
	ruleMutationPredicate      = "mutation-requires-predicate"
	ruleExplicitInsertColumns  = "explicit-insert-columns"
	ruleExplicitJoins          = "explicit-joins"
	ruleExplicitDerivedAliases = "explicit-derived-relation-aliases"
	ruleLimitedCollectionOrder = "limited-collection-requires-order"
	ruleNoOffsetPagination     = "no-offset-pagination"
	ruleProductDMLOnly         = "product-dml-only"
	ruleNamedQueryRequired     = "named-query-required"
	ruleQueryOwnership         = "query-ownership-matches-sqlc"
	ruleBlockingLockWithTime   = "blocking-lock-with-database-time"
	ruleParseError             = "parse-error"
)

type issue struct {
	Path    string
	Line    int
	Column  int
	Rule    string
	Message string
}

type queryMetadata struct {
	name       string
	command    string
	sqlStart   int32
	diagnostic int32
}

type queryOwnershipError struct {
	message string
}

func (err *queryOwnershipError) Error() string {
	return err.message
}

type queryChecker struct {
	path            string
	source          []byte
	name            string
	command         string
	defaultLocation int32
	issues          []issue
}

func checkDirectory(root string) ([]issue, error) {
	var paths []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".sql" {
			return nil
		}
		paths = append(paths, path)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk SQL queries: %w", err)
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("no SQL query files found in %s", root)
	}
	sort.Strings(paths)

	var issues []issue
	for _, path := range paths {
		fileIssues, err := checkFile(path)
		if err != nil {
			return nil, err
		}
		issues = append(issues, fileIssues...)
	}
	sort.Slice(issues, func(i, j int) bool {
		left, right := issues[i], issues[j]
		if left.Path != right.Path {
			return left.Path < right.Path
		}
		if left.Line != right.Line {
			return left.Line < right.Line
		}
		if left.Column != right.Column {
			return left.Column < right.Column
		}
		return left.Rule < right.Rule
	})
	return issues, nil
}

func checkFile(path string) ([]issue, error) {
	source, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return checkSource(path, source), nil
}

func checkSource(path string, source []byte) []issue {
	checker := queryChecker{path: path, source: source, name: "<file>"}
	tree, err := pgquery.Parse(string(source))
	if err != nil {
		location := int32(0)
		var parseErr *pgparser.Error
		if errors.As(err, &parseErr) && parseErr.Cursorpos > 0 {
			location = int32(parseErr.Cursorpos - 1)
		}
		checker.report(ruleParseError, err.Error(), location)
		return checker.issues
	}

	for _, rawStatement := range tree.GetStmts() {
		metadata, metadataErr := parseQueryMetadata(source, rawStatement)
		statementChecker := queryChecker{
			path:            path,
			source:          source,
			name:            metadata.name,
			command:         metadata.command,
			defaultLocation: metadata.sqlStart,
		}
		if statementChecker.name == "" {
			statementChecker.name = "<unnamed>"
		}
		if metadataErr != nil {
			rule := ruleNamedQueryRequired
			var ownershipErr *queryOwnershipError
			if errors.As(metadataErr, &ownershipErr) {
				rule = ruleQueryOwnership
			}
			statementChecker.report(rule, metadataErr.Error(), metadata.diagnostic)
		} else if metadata.name == "" {
			statementChecker.report(
				ruleNamedQueryRequired,
				"every product SQL statement must belong to a named sqlc query",
				metadata.sqlStart,
			)
		}
		statementChecker.checkStatement(rawStatement)
		checker.issues = append(checker.issues, statementChecker.issues...)
	}
	return checker.issues
}

func parseQueryMetadata(source []byte, statement *pg_query.RawStmt) (queryMetadata, error) {
	start := max(0, min(int(statement.GetStmtLocation()), len(source)))
	end := len(source)
	if statement.GetStmtLen() > 0 {
		end = min(start+int(statement.GetStmtLen()), len(source))
	}
	metadata := queryMetadata{
		sqlStart:   int32(start),
		diagnostic: int32(start),
	}
	body := source[start:end]
	for offset := 0; offset < len(body); {
		if isSQLSpace(body[offset]) {
			offset++
			continue
		}

		lineStart := bytes.LastIndexByte(body[:offset], '\n') + 1
		unindented := offset == lineStart
		switch {
		case bytes.HasPrefix(body[offset:], []byte("--")):
			commentEnd := len(body)
			if newline := bytes.IndexByte(body[offset:], '\n'); newline >= 0 {
				commentEnd = offset + newline
			}
			if unindented && metadata.name == "" {
				name, command, candidate, err := parseQueryAnnotation(string(body[offset:commentEnd]))
				if err != nil {
					metadata.diagnostic = int32(start + offset)
					return metadata, err
				}
				if candidate {
					metadata.diagnostic = int32(start + offset)
					metadata.name = name
					metadata.command = command
				}
			}
			offset = commentEnd
		case bytes.HasPrefix(body[offset:], []byte("/*")):
			commentEnd, ok := leadingBlockCommentEnd(body, offset)
			if !ok {
				metadata.diagnostic = int32(start + offset)
				return metadata, errors.New("unterminated leading SQL comment")
			}
			comment := body[offset:commentEnd]
			if unindented && metadata.name == "" && !bytes.ContainsAny(comment, "\r\n") {
				name, command, candidate, err := parseQueryAnnotation(string(comment))
				if err != nil {
					metadata.diagnostic = int32(start + offset)
					return metadata, err
				}
				if candidate {
					metadata.diagnostic = int32(start + offset)
					metadata.name = name
					metadata.command = command
				}
			}
			offset = commentEnd
		default:
			metadata.sqlStart = int32(start + offset)
			return reconcileQueryMetadata(body, start, metadata)
		}
	}
	metadata.sqlStart = int32(end)
	return reconcileQueryMetadata(body, start, metadata)
}

func reconcileQueryMetadata(body []byte, start int, metadata queryMetadata) (queryMetadata, error) {
	sqlcMetadata, err := parseSQLCQueryMetadata(body, start)
	if err != nil {
		metadata.diagnostic = sqlcMetadata.diagnostic
		return metadata, &queryOwnershipError{message: fmt.Sprintf("sqlc rejects query ownership metadata: %v", err)}
	}
	if metadata.name == sqlcMetadata.name && metadata.command == sqlcMetadata.command {
		return metadata, nil
	}
	if sqlcMetadata.name != "" {
		metadata.diagnostic = sqlcMetadata.diagnostic
	}
	return metadata, &queryOwnershipError{message: fmt.Sprintf(
		"SQL syntax resolves owner as %q %s, but sqlc resolves it as %q %s",
		metadata.name,
		metadata.command,
		sqlcMetadata.name,
		sqlcMetadata.command,
	)}
}

func parseSQLCQueryMetadata(body []byte, start int) (queryMetadata, error) {
	metadata := queryMetadata{diagnostic: int32(start)}
	for offset := 0; offset <= len(body); {
		lineEnd := len(body)
		if newline := bytes.IndexByte(body[offset:], '\n'); newline >= 0 {
			lineEnd = offset + newline
		}
		line := body[offset:lineEnd]
		if bytes.HasPrefix(line, []byte("--")) || bytes.HasPrefix(line, []byte("/*")) {
			name, command, candidate, err := parseQueryAnnotation(string(line))
			if candidate {
				metadata.diagnostic = int32(start + offset)
				if err != nil {
					return metadata, err
				}
				metadata.name = name
				metadata.command = command
				return metadata, nil
			}
		}
		if lineEnd == len(body) {
			break
		}
		offset = lineEnd + 1
	}
	return metadata, nil
}

func leadingBlockCommentEnd(source []byte, start int) (int, bool) {
	depth := 1
	for offset := start + 2; offset+1 < len(source); {
		switch {
		case source[offset] == '/' && source[offset+1] == '*':
			depth++
			offset += 2
		case source[offset] == '*' && source[offset+1] == '/':
			depth--
			offset += 2
			if depth == 0 {
				return offset, true
			}
		default:
			offset++
		}
	}
	return 0, false
}

func parseQueryAnnotation(comment string) (string, string, bool, error) {
	prefix := "--"
	if strings.HasPrefix(comment, "/*") {
		prefix = "/*"
	}
	rest := comment[len(prefix):]
	if !strings.HasPrefix(strings.TrimSpace(rest), "name") || !strings.Contains(rest, ":") {
		return "", "", false, nil
	}
	if !strings.HasPrefix(rest, " name: ") {
		return "", "", true, fmt.Errorf("invalid sqlc query annotation: %s", comment)
	}

	parts := strings.Split(strings.TrimSpace(comment), " ")
	if prefix == "/*" {
		if len(parts) == 0 || parts[len(parts)-1] != "*/" {
			return "", "", true, fmt.Errorf("invalid sqlc query annotation: %s", comment)
		}
		parts = parts[:len(parts)-1]
	}
	if len(parts) != 4 {
		return "", "", true, fmt.Errorf("invalid sqlc query annotation: %s", comment)
	}

	name := parts[2]
	if !validQueryName(name) {
		return "", "", true, fmt.Errorf("invalid sqlc query name %q", name)
	}
	command := parts[3]
	switch command {
	case ":one", ":many", ":exec", ":execresult", ":execrows", ":execlastid", ":copyfrom",
		":batchexec", ":batchmany", ":batchone":
		return name, command, true, nil
	default:
		return "", "", true, fmt.Errorf("invalid sqlc query command %q", command)
	}
}

func validQueryName(name string) bool {
	if name == "" {
		return false
	}
	for index, character := range name {
		letter := unicode.IsLetter(character) || character == '_'
		if !letter && !(index > 0 && unicode.IsDigit(character)) {
			return false
		}
	}
	return true
}

func isSQLSpace(character byte) bool {
	switch character {
	case ' ', '\t', '\r', '\n', '\f':
		return true
	default:
		return false
	}
}

func (checker *queryChecker) checkStatement(rawStatement *pg_query.RawStmt) {
	statement := rawStatement.GetStmt()
	if !isProductDML(statement) {
		checker.report(
			ruleProductDMLOnly,
			"product query files may contain only SELECT, INSERT, UPDATE, and DELETE statements",
			rawStatement.GetStmtLocation(),
		)
	}
	if checker.command == ":many" || checker.command == ":batchmany" {
		checker.checkTopLevelCollection(statement)
	}
	checker.checkBlockingLockWithDatabaseTime(statement)
	walkMessage(statement.ProtoReflect(), nil, checker.checkMessage)
}

func (checker *queryChecker) checkBlockingLockWithDatabaseTime(statement *pg_query.Node) {
	if !isMutationStatement(statement) {
		return
	}
	blockingLock := false
	hasDatabaseTime := false
	timeLocation := int32(0)
	walkMessage(statement.ProtoReflect(), nil, func(message protoreflect.Message, _ []protoreflect.Message) {
		switch node := message.Interface().(type) {
		case *pg_query.LockingClause:
			blockingLock = blockingLock || lockingClauseCanBlock(node)
		case *pg_query.FuncCall:
			if !hasDatabaseTime && isExplicitDatabaseTimeCall(node) {
				hasDatabaseTime = true
				timeLocation = node.GetLocation()
			}
		}
	})
	if blockingLock && hasDatabaseTime {
		checker.report(
			ruleBlockingLockWithTime,
			"acquire blocking row locks in a prior statement before sampling database time in a mutation",
			timeLocation,
		)
	}
}

func (checker *queryChecker) checkTopLevelCollection(statement *pg_query.Node) {
	selection := statement.GetSelectStmt()
	if selection == nil || !hasFiniteLimit(selection.GetLimitCount()) || len(selection.GetSortClause()) != 0 {
		return
	}
	checker.report(
		ruleLimitedCollectionOrder,
		"top-level limited collection queries must define ORDER BY",
		messageLocation(selection.GetLimitCount().ProtoReflect()),
	)
}

func (checker *queryChecker) checkMessage(
	message protoreflect.Message,
	ancestors []protoreflect.Message,
) {
	switch node := message.Interface().(type) {
	case *pg_query.SelectStmt:
		checker.checkOutputTargets(node.GetTargetList(), visibleRelationNames(node.GetFromClause()))
		checker.checkRelationList(node.GetFromClause())
		if node.GetLimitOffset() != nil {
			checker.report(
				ruleNoOffsetPagination,
				"product storage queries must use keyset pagination instead of OFFSET",
				messageLocation(node.GetLimitOffset().ProtoReflect()),
			)
		}
	case *pg_query.InsertStmt:
		if len(node.GetCols()) == 0 && node.GetSelectStmt() != nil {
			checker.report(
				ruleExplicitInsertColumns,
				"INSERT statements must declare their target columns",
				node.GetRelation().GetLocation(),
			)
		}
		checker.checkInsertDatabaseOwnedTime(node)
		if conflict := node.GetOnConflictClause(); conflict != nil {
			checker.checkUpdateDatabaseOwnedTime(conflict.GetTargetList())
		}
		checker.checkOutputTargets(node.GetReturningList(), mutationRelationNames(node.GetRelation(), nil))
	case *pg_query.UpdateStmt:
		if node.GetWhereClause() == nil {
			checker.report(
				ruleMutationPredicate,
				"UPDATE statements must include a WHERE predicate",
				node.GetRelation().GetLocation(),
			)
		}
		checker.checkUpdateDatabaseOwnedTime(node.GetTargetList())
		checker.checkRelationList(node.GetFromClause())
		checker.checkOutputTargets(
			node.GetReturningList(),
			mutationRelationNames(node.GetRelation(), node.GetFromClause()),
		)
	case *pg_query.DeleteStmt:
		if node.GetWhereClause() == nil {
			checker.report(
				ruleMutationPredicate,
				"DELETE statements must include a WHERE predicate",
				node.GetRelation().GetLocation(),
			)
		}
		checker.checkRelationList(node.GetUsingClause())
		checker.checkOutputTargets(
			node.GetReturningList(),
			mutationRelationNames(node.GetRelation(), node.GetUsingClause()),
		)
	case *pg_query.JoinExpr:
		if node.GetIsNatural() {
			checker.report(
				ruleExplicitJoins,
				"NATURAL JOIN is not allowed; declare the join relationship explicitly",
				messageLocation(node.GetRarg().ProtoReflect()),
			)
		}
	case *pg_query.FuncCall:
		checker.checkFunctionCall(node)
	case *pg_query.SQLValueFunction:
		if isCurrentTimeValue(node.GetOp()) {
			checker.report(
				ruleExplicitDurableTime,
				"use transaction_timestamp() or statement_timestamp() as the explicit durable time source",
				node.GetLocation(),
			)
		}
	case *pg_query.A_Const:
		if isRelativeTimeConstant(node) &&
			relativeTimeLiteralHasDirectTemporalConversion(node, ancestors) &&
			!literalBelongsToSQLCParameter(ancestors) {
			checker.report(
				ruleExplicitDurableTime,
				"PostgreSQL relative-time values must use transaction_timestamp(), statement_timestamp(), or a semantic parameter",
				messageLocation(node.ProtoReflect()),
			)
		}
	case *pg_query.A_Expr:
		if isAtNowParameter(node) {
			checker.report(
				ruleNoApplicationNow,
				"storage queries must not accept an application parameter named now",
				node.GetLocation(),
			)
		}
	}
}

func (checker *queryChecker) checkUpdateDatabaseOwnedTime(targets []*pg_query.Node) {
	for _, targetNode := range targets {
		target := targetNode.GetResTarget()
		if target == nil || !isDatabaseOwnedTimeColumn(target.GetName()) ||
			!expressionContainsApplicationTime(target.GetVal()) {
			continue
		}
		checker.report(
			ruleDatabaseOwnedTime,
			fmt.Sprintf(
				"database-owned time column %s must be derived by PostgreSQL; use a source_* column for external evidence",
				target.GetName(),
			),
			target.GetLocation(),
		)
	}
}

func (checker *queryChecker) checkInsertDatabaseOwnedTime(statement *pg_query.InsertStmt) {
	columns := make([]string, 0, len(statement.GetCols()))
	for _, columnNode := range statement.GetCols() {
		column := columnNode.GetResTarget()
		if column == nil {
			columns = append(columns, "")
			continue
		}
		columns = append(columns, column.GetName())
	}
	selection := statement.GetSelectStmt().GetSelectStmt()
	if len(columns) == 0 || selection == nil {
		return
	}
	checker.checkInsertSelectionDatabaseOwnedTime(columns, selection)
}

func (checker *queryChecker) checkInsertSelectionDatabaseOwnedTime(columns []string, selection *pg_query.SelectStmt) {
	if selection == nil {
		return
	}
	if selection.GetLarg() != nil || selection.GetRarg() != nil {
		checker.checkInsertSelectionDatabaseOwnedTime(columns, selection.GetLarg())
		checker.checkInsertSelectionDatabaseOwnedTime(columns, selection.GetRarg())
		return
	}
	for _, valuesNode := range selection.GetValuesLists() {
		checker.checkInsertedDatabaseOwnedTime(columns, valuesNode.GetList().GetItems())
	}
	if len(selection.GetTargetList()) == 0 {
		return
	}
	expressions := make([]*pg_query.Node, 0, len(selection.GetTargetList()))
	for _, targetNode := range selection.GetTargetList() {
		target := targetNode.GetResTarget()
		if target == nil {
			expressions = append(expressions, nil)
			continue
		}
		expressions = append(expressions, target.GetVal())
	}
	checker.checkInsertedDatabaseOwnedTime(columns, expressions)
}

func (checker *queryChecker) checkInsertedDatabaseOwnedTime(columns []string, expressions []*pg_query.Node) {
	for index, column := range columns {
		if index >= len(expressions) || !isDatabaseOwnedTimeColumn(column) ||
			!expressionContainsApplicationTime(expressions[index]) {
			continue
		}
		checker.report(
			ruleDatabaseOwnedTime,
			fmt.Sprintf(
				"database-owned time column %s must be derived by PostgreSQL; use a source_* column for external evidence",
				column,
			),
			messageLocation(expressions[index].ProtoReflect()),
		)
	}
}

func (checker *queryChecker) checkOutputTargets(targets []*pg_query.Node, relationNames map[string]struct{}) {
	for _, targetNode := range targets {
		target := targetNode.GetResTarget()
		if target == nil || !projectionContainsImplicitShape(target.GetVal(), relationNames) {
			continue
		}
		checker.report(
			ruleExplicitOutputColumns,
			"query outputs must list columns explicitly instead of using wildcard or whole-row projections",
			target.GetLocation(),
		)
	}
}

func (checker *queryChecker) checkRelationList(relations []*pg_query.Node) {
	if len(relations) >= 2 {
		checker.report(
			ruleExplicitJoins,
			"comma joins are not allowed; use an explicit JOIN or CROSS JOIN",
			messageLocation(relations[1].ProtoReflect()),
		)
	}
	for _, relation := range relations {
		checker.checkRelationShape(relation)
	}
}

func (checker *queryChecker) checkFunctionCall(call *pg_query.FuncCall) {
	name := nodePath(call.GetFuncname())
	if isBuiltinFunctionPath(name) {
		switch name[len(name)-1] {
		case "now", "clock_timestamp", "timeofday":
			checker.report(
				ruleExplicitDurableTime,
				"use transaction_timestamp() or statement_timestamp() as the explicit durable time source",
				call.GetLocation(),
			)
		case "age":
			if len(call.GetArgs()) == 1 {
				checker.report(
					ruleExplicitDurableTime,
					"one-argument age() implicitly uses the current date; pass both timestamps explicitly",
					call.GetLocation(),
				)
			}
		}
	}
	if isSQLCNowParameter(call) {
		checker.report(
			ruleNoApplicationNow,
			"storage queries must not accept an application parameter named now",
			call.GetLocation(),
		)
	}
}

func (checker *queryChecker) checkRelationShape(relation *pg_query.Node) {
	if relation == nil {
		return
	}
	switch {
	case relation.GetRangeVar() != nil:
		return
	case relation.GetJoinExpr() != nil:
		join := relation.GetJoinExpr()
		checker.checkRelationShape(join.GetLarg())
		checker.checkRelationShape(join.GetRarg())
	case relation.GetRangeSubselect() != nil:
		checker.requireDerivedRelationAlias(relation.GetRangeSubselect().GetAlias(), relation)
	case relation.GetRangeFunction() != nil:
		checker.requireDerivedRelationAlias(relation.GetRangeFunction().GetAlias(), relation)
	case relation.GetRangeTableFunc() != nil:
		checker.requireDerivedRelationAlias(relation.GetRangeTableFunc().GetAlias(), relation)
	case relation.GetJsonTable() != nil:
		checker.requireDerivedRelationAlias(relation.GetJsonTable().GetAlias(), relation)
	case relation.GetRangeTableSample() != nil:
		checker.checkRelationShape(relation.GetRangeTableSample().GetRelation())
	default:
		checker.report(
			ruleExplicitDerivedAliases,
			"unsupported FROM relation shape; use an explicitly aliased table, subquery, function, or join",
			messageLocation(relation.ProtoReflect()),
		)
	}
}

func (checker *queryChecker) requireDerivedRelationAlias(alias *pg_query.Alias, relation *pg_query.Node) {
	if alias != nil && alias.GetAliasname() != "" {
		return
	}
	checker.report(
		ruleExplicitDerivedAliases,
		"derived relations must declare an explicit alias",
		messageLocation(relation.ProtoReflect()),
	)
}

func (checker *queryChecker) report(rule, message string, location int32) {
	offset := int(checker.defaultLocation)
	if location > 0 {
		offset = int(location)
	}
	line, column := sourcePosition(checker.source, offset)
	checker.issues = append(checker.issues, issue{
		Path:    diagnosticPath(checker.path),
		Line:    line,
		Column:  column,
		Rule:    rule,
		Message: fmt.Sprintf("%s: %s", checker.name, message),
	})
}

func diagnosticPath(path string) string {
	path = filepath.ToSlash(path)
	const queryPath = "internal/storage/queries/"
	if index := strings.LastIndex(path, queryPath); index >= 0 {
		return path[index:]
	}
	return path
}

func walkMessage(
	message protoreflect.Message,
	ancestors []protoreflect.Message,
	visit func(protoreflect.Message, []protoreflect.Message),
) {
	if !message.IsValid() {
		return
	}
	visit(message, ancestors)
	ancestors = append(ancestors, message)
	message.Range(func(field protoreflect.FieldDescriptor, value protoreflect.Value) bool {
		if field.IsMap() {
			if field.MapValue().Kind() != protoreflect.MessageKind {
				return true
			}
			value.Map().Range(func(_ protoreflect.MapKey, item protoreflect.Value) bool {
				walkMessage(item.Message(), ancestors, visit)
				return true
			})
			return true
		}
		if field.IsList() {
			if field.Kind() != protoreflect.MessageKind && field.Kind() != protoreflect.GroupKind {
				return true
			}
			list := value.List()
			for index := range list.Len() {
				walkMessage(list.Get(index).Message(), ancestors, visit)
			}
			return true
		}
		if field.Kind() == protoreflect.MessageKind || field.Kind() == protoreflect.GroupKind {
			walkMessage(value.Message(), ancestors, visit)
		}
		return true
	})
}

func isProductDML(statement *pg_query.Node) bool {
	selection := statement.GetSelectStmt()
	return (selection != nil && selection.GetIntoClause() == nil) || statement.GetInsertStmt() != nil ||
		statement.GetUpdateStmt() != nil ||
		statement.GetDeleteStmt() != nil
}

func isMutationStatement(statement *pg_query.Node) bool {
	return statement.GetInsertStmt() != nil || statement.GetUpdateStmt() != nil ||
		statement.GetDeleteStmt() != nil
}

func lockingClauseCanBlock(clause *pg_query.LockingClause) bool {
	if clause.GetStrength() == pg_query.LockClauseStrength_LCS_NONE ||
		clause.GetStrength() == pg_query.LockClauseStrength_LOCK_CLAUSE_STRENGTH_UNDEFINED {
		return false
	}
	return clause.GetWaitPolicy() != pg_query.LockWaitPolicy_LockWaitSkip &&
		clause.GetWaitPolicy() != pg_query.LockWaitPolicy_LockWaitError
}

func isExplicitDatabaseTimeCall(call *pg_query.FuncCall) bool {
	name := nodePath(call.GetFuncname())
	if !isBuiltinFunctionPath(name) {
		return false
	}
	return name[len(name)-1] == "statement_timestamp" || name[len(name)-1] == "transaction_timestamp"
}

func projectionContainsImplicitShape(expression *pg_query.Node, relationNames map[string]struct{}) bool {
	if expression == nil {
		return false
	}
	found := false
	walkMessage(expression.ProtoReflect(), nil, func(candidate protoreflect.Message, ancestors []protoreflect.Message) {
		if found || hasSelectAncestor(ancestors) {
			return
		}
		switch node := candidate.Interface().(type) {
		case *pg_query.A_Star:
			found = true
		case *pg_query.FuncCall:
			found = isSQLCEmbedCall(node)
		case *pg_query.ColumnRef:
			path := nodePath(node.GetFields())
			if len(path) == 1 {
				_, found = relationNames[path[0]]
			}
		}
	})
	return found
}

func mutationRelationNames(target *pg_query.RangeVar, additional []*pg_query.Node) map[string]struct{} {
	names := visibleRelationNames(additional)
	addRangeVarName(names, target)
	return names
}

func visibleRelationNames(relations []*pg_query.Node) map[string]struct{} {
	names := make(map[string]struct{})
	for _, relation := range relations {
		addVisibleRelationNames(names, relation)
	}
	return names
}

func addVisibleRelationNames(names map[string]struct{}, relation *pg_query.Node) {
	if relation == nil {
		return
	}
	switch {
	case relation.GetRangeVar() != nil:
		addRangeVarName(names, relation.GetRangeVar())
	case relation.GetJoinExpr() != nil:
		join := relation.GetJoinExpr()
		if addAliasName(names, join.GetAlias()) {
			return
		}
		addVisibleRelationNames(names, join.GetLarg())
		addVisibleRelationNames(names, join.GetRarg())
		addAliasName(names, join.GetJoinUsingAlias())
	case relation.GetRangeSubselect() != nil:
		addAliasName(names, relation.GetRangeSubselect().GetAlias())
	case relation.GetRangeFunction() != nil:
		addAliasName(names, relation.GetRangeFunction().GetAlias())
	case relation.GetRangeTableFunc() != nil:
		addAliasName(names, relation.GetRangeTableFunc().GetAlias())
	case relation.GetJsonTable() != nil:
		addAliasName(names, relation.GetJsonTable().GetAlias())
	case relation.GetRangeTableSample() != nil:
		addVisibleRelationNames(names, relation.GetRangeTableSample().GetRelation())
	}
}

func addRangeVarName(names map[string]struct{}, relation *pg_query.RangeVar) {
	if relation == nil || addAliasName(names, relation.GetAlias()) {
		return
	}
	if relation.GetRelname() != "" {
		names[strings.ToLower(relation.GetRelname())] = struct{}{}
	}
}

func addAliasName(names map[string]struct{}, alias *pg_query.Alias) bool {
	if alias == nil || alias.GetAliasname() == "" {
		return false
	}
	names[strings.ToLower(alias.GetAliasname())] = struct{}{}
	return true
}

func hasSelectAncestor(ancestors []protoreflect.Message) bool {
	for _, ancestor := range ancestors {
		if _, ok := ancestor.Interface().(*pg_query.SelectStmt); ok {
			return true
		}
	}
	return false
}

func hasFiniteLimit(limit *pg_query.Node) bool {
	if limit == nil {
		return false
	}
	constant := limit.GetAConst()
	return constant == nil || !constant.GetIsnull()
}

func isCurrentTimeValue(operation pg_query.SQLValueFunctionOp) bool {
	switch operation {
	case pg_query.SQLValueFunctionOp_SVFOP_CURRENT_DATE,
		pg_query.SQLValueFunctionOp_SVFOP_CURRENT_TIME,
		pg_query.SQLValueFunctionOp_SVFOP_CURRENT_TIME_N,
		pg_query.SQLValueFunctionOp_SVFOP_CURRENT_TIMESTAMP,
		pg_query.SQLValueFunctionOp_SVFOP_CURRENT_TIMESTAMP_N,
		pg_query.SQLValueFunctionOp_SVFOP_LOCALTIME,
		pg_query.SQLValueFunctionOp_SVFOP_LOCALTIME_N,
		pg_query.SQLValueFunctionOp_SVFOP_LOCALTIMESTAMP,
		pg_query.SQLValueFunctionOp_SVFOP_LOCALTIMESTAMP_N:
		return true
	default:
		return false
	}
}

func isRelativeTimeConstant(constant *pg_query.A_Const) bool {
	if constant.GetSval() == nil {
		return false
	}
	fields := strings.Fields(strings.ToLower(constant.GetSval().GetSval()))
	if len(fields) == 0 {
		return false
	}
	switch fields[0] {
	case "now", "today", "tomorrow", "yesterday":
		return true
	default:
		return false
	}
}

func relativeTimeLiteralHasDirectTemporalConversion(
	constant *pg_query.A_Const,
	ancestors []protoreflect.Message,
) bool {
	for index := len(ancestors) - 1; index >= 0; index-- {
		switch node := ancestors[index].Interface().(type) {
		case *pg_query.Node:
			continue
		case *pg_query.TypeCast:
			if isTemporalTypeName(node.GetTypeName()) {
				return true
			}
			continue
		case *pg_query.CollateClause, *pg_query.CoalesceExpr, *pg_query.MinMaxExpr:
			continue
		case *pg_query.CaseWhen:
			if expressionContainsConstant(node.GetResult(), constant) {
				continue
			}
			return false
		case *pg_query.CaseExpr:
			if caseResultContainsConstant(node, constant) {
				continue
			}
			return false
		case *pg_query.FuncCall:
			return isTemporalConversionFunction(node)
		default:
			return false
		}
	}
	return false
}

func caseResultContainsConstant(expression *pg_query.CaseExpr, constant *pg_query.A_Const) bool {
	if expressionContainsConstant(expression.GetDefresult(), constant) {
		return true
	}
	for _, argument := range expression.GetArgs() {
		when := argument.GetCaseWhen()
		if when != nil && expressionContainsConstant(when.GetResult(), constant) {
			return true
		}
	}
	return false
}

func expressionContainsConstant(expression *pg_query.Node, constant *pg_query.A_Const) bool {
	if expression == nil {
		return false
	}
	found := false
	walkMessage(expression.ProtoReflect(), nil, func(candidate protoreflect.Message, _ []protoreflect.Message) {
		if node, ok := candidate.Interface().(*pg_query.A_Const); ok && node == constant {
			found = true
		}
	})
	return found
}

func isTemporalTypeName(name *pg_query.TypeName) bool {
	path := nodePath(name.GetNames())
	if len(path) == 0 {
		return false
	}
	if !isBuiltinFunctionPath(path) {
		return false
	}
	switch path[len(path)-1] {
	case "date", "time", "timetz", "timestamp", "timestamptz":
		return true
	default:
		return false
	}
}

func isTemporalConversionFunction(call *pg_query.FuncCall) bool {
	path := nodePath(call.GetFuncname())
	if !isBuiltinFunctionPath(path) {
		return false
	}
	switch path[len(path)-1] {
	case "date", "time", "timetz", "timestamp", "timestamptz":
		return true
	default:
		return false
	}
}

func literalBelongsToSQLCParameter(ancestors []protoreflect.Message) bool {
	for index := len(ancestors) - 1; index >= 0; index-- {
		call, ok := ancestors[index].Interface().(*pg_query.FuncCall)
		if ok {
			return isSQLCParameterCall(call)
		}
	}
	return false
}

func isBuiltinFunctionPath(path []string) bool {
	return len(path) == 1 || (len(path) == 2 && path[0] == "pg_catalog")
}

func isSQLCNowParameter(call *pg_query.FuncCall) bool {
	if !isSQLCParameterCall(call) {
		return false
	}
	arguments := call.GetArgs()
	return len(arguments) == 1 && isNowParameterName(arguments[0])
}

func isSQLCParameterCall(call *pg_query.FuncCall) bool {
	name := nodePath(call.GetFuncname())
	return len(name) == 2 && name[0] == "sqlc" && (name[1] == "arg" || name[1] == "narg")
}

func isSQLCEmbedCall(call *pg_query.FuncCall) bool {
	name := nodePath(call.GetFuncname())
	return len(name) == 2 && name[0] == "sqlc" && name[1] == "embed"
}

func isDatabaseOwnedTimeColumn(name string) bool {
	name = strings.ToLower(name)
	return strings.HasSuffix(name, "_at") && !strings.HasPrefix(name, "source_")
}

func expressionContainsApplicationTime(expression *pg_query.Node) bool {
	if expression == nil {
		return false
	}
	if call := expression.GetFuncCall(); call != nil {
		if isSQLCParameterCall(call) {
			return true
		}
		if isDurationConstructor(call) {
			return false
		}
		return anyExpressionContainsApplicationTime(call.GetArgs())
	}
	if expression.GetParamRef() != nil {
		return true
	}
	if operation := expression.GetAExpr(); operation != nil {
		if isAtParameter(operation) {
			return true
		}
		if isDurationArithmetic(operation) {
			return false
		}
		return expressionContainsApplicationTime(operation.GetLexpr()) ||
			expressionContainsApplicationTime(operation.GetRexpr())
	}
	if cast := expression.GetTypeCast(); cast != nil {
		if typeNameEndsWith(cast.GetTypeName(), "interval") {
			return false
		}
		return expressionContainsApplicationTime(cast.GetArg())
	}
	if collate := expression.GetCollateClause(); collate != nil {
		return expressionContainsApplicationTime(collate.GetArg())
	}
	if coalesce := expression.GetCoalesceExpr(); coalesce != nil {
		return anyExpressionContainsApplicationTime(coalesce.GetArgs())
	}
	if minimumOrMaximum := expression.GetMinMaxExpr(); minimumOrMaximum != nil {
		return anyExpressionContainsApplicationTime(minimumOrMaximum.GetArgs())
	}
	if caseExpression := expression.GetCaseExpr(); caseExpression != nil {
		if expressionContainsApplicationTime(caseExpression.GetDefresult()) {
			return true
		}
		for _, whenNode := range caseExpression.GetArgs() {
			when := whenNode.GetCaseWhen()
			if when != nil && expressionContainsApplicationTime(when.GetResult()) {
				return true
			}
		}
		return false
	}

	found := false
	walkMessage(expression.ProtoReflect(), nil, func(candidate protoreflect.Message, _ []protoreflect.Message) {
		if found {
			return
		}
		switch node := candidate.Interface().(type) {
		case *pg_query.FuncCall:
			found = isSQLCParameterCall(node)
		case *pg_query.ParamRef:
			found = true
		case *pg_query.A_Expr:
			found = isAtParameter(node)
		}
	})
	return found
}

func anyExpressionContainsApplicationTime(expressions []*pg_query.Node) bool {
	for _, expression := range expressions {
		if expressionContainsApplicationTime(expression) {
			return true
		}
	}
	return false
}

func isDurationConstructor(call *pg_query.FuncCall) bool {
	name := nodePath(call.GetFuncname())
	return len(name) != 0 && name[len(name)-1] == "make_interval"
}

func isDurationArithmetic(expression *pg_query.A_Expr) bool {
	operator := nodePath(expression.GetName())
	if len(operator) != 1 || (operator[0] != "*" && operator[0] != "/") {
		return false
	}
	return expressionContainsInterval(expression.GetLexpr()) || expressionContainsInterval(expression.GetRexpr())
}

func expressionContainsInterval(expression *pg_query.Node) bool {
	if expression == nil {
		return false
	}
	found := false
	walkMessage(expression.ProtoReflect(), nil, func(candidate protoreflect.Message, _ []protoreflect.Message) {
		if found {
			return
		}
		switch node := candidate.Interface().(type) {
		case *pg_query.TypeCast:
			found = typeNameEndsWith(node.GetTypeName(), "interval")
		case *pg_query.FuncCall:
			found = isDurationConstructor(node)
		}
	})
	return found
}

func typeNameEndsWith(name *pg_query.TypeName, suffix string) bool {
	path := nodePath(name.GetNames())
	return len(path) != 0 && path[len(path)-1] == suffix
}

func isAtParameter(expression *pg_query.A_Expr) bool {
	return expression.GetLexpr() == nil &&
		len(expression.GetName()) == 1 &&
		strings.EqualFold(expression.GetName()[0].GetString_().GetSval(), "@")
}

func isAtNowParameter(expression *pg_query.A_Expr) bool {
	return isAtParameter(expression) &&
		isNowParameterName(expression.GetRexpr())
}

func isNowParameterName(node *pg_query.Node) bool {
	reference := node.GetColumnRef()
	if reference != nil {
		path := nodePath(reference.GetFields())
		return len(path) == 1 && path[0] == "now"
	}
	constant := node.GetAConst()
	return constant != nil && constant.GetSval() != nil &&
		strings.EqualFold(strings.TrimSpace(constant.GetSval().GetSval()), "now")
}

func nodePath(nodes []*pg_query.Node) []string {
	path := make([]string, 0, len(nodes))
	for _, node := range nodes {
		value := node.GetString_()
		if value == nil {
			return nil
		}
		path = append(path, strings.ToLower(value.GetSval()))
	}
	return path
}

func messageLocation(message protoreflect.Message) int32 {
	if !message.IsValid() {
		return 0
	}
	location := int32(0)
	walkMessage(message, nil, func(candidate protoreflect.Message, _ []protoreflect.Message) {
		locationField := candidate.Descriptor().Fields().ByName("location")
		if locationField == nil {
			return
		}
		candidateLocation := int32(candidate.Get(locationField).Int())
		if candidateLocation > 0 && (location == 0 || candidateLocation < location) {
			location = candidateLocation
		}
	})
	return location
}

func sourcePosition(source []byte, offset int) (int, int) {
	offset = max(0, min(offset, len(source)))
	line := bytes.Count(source[:offset], []byte{'\n'}) + 1
	lineStart := bytes.LastIndexByte(source[:offset], '\n') + 1
	return line, offset - lineStart + 1
}
