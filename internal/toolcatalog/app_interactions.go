package toolcatalog

const (
	ToolNameListInteractionHandlers = "list_interaction_handlers"
	ToolNameSetInteractionHandler   = "set_interaction_handler"
	InteractionHandlersDefaultLimit = 20
	InteractionHandlersMaxLimit     = 100
)

func InteractionHandlerToolNames() []string {
	return []string{ToolNameListInteractionHandlers, ToolNameSetInteractionHandler}
}
func IsInteractionHandlerTool(name string) bool {
	return name == ToolNameListInteractionHandlers || name == ToolNameSetInteractionHandler
}
func interactionHandlerTools() ([]Entry, error) {
	list, err := toolEntry(ToolNameListInteractionHandlers,
		"List configured handlers for future questions and approval prompts. Returns handler keys, safe descriptions, "+
			"effective argument schemas, and the current selection including args independently of the page. "+
			"Pass next_cursor as cursor to continue listing. Null selection means dashboard only.", nil, map[string]any{
			"cursor": map[string]any{"type": "string", "minLength": 1},
			"limit":  map[string]any{"type": "integer", "minimum": 1, "maximum": InteractionHandlersMaxLimit},
		})
	if err != nil {
		return nil, err
	}
	set, err := toolEntry(ToolNameSetInteractionHandler,
		"Select a configured handler and its destination args for future questions and approval prompts. "+
			"Use the selected handler's argument schema from list_interaction_handlers. "+
			"Pass handler: null and args: {} for dashboard only. Existing prompts retain their destination; the dashboard remains available.",
		[]string{"handler", "args"}, map[string]any{
			"handler": map[string]any{
				"anyOf": []any{map[string]any{"type": "null"}, map[string]any{"type": "string", "pattern": AppNamePattern}},
			},
			"args": map[string]any{"type": "object", "additionalProperties": true},
		})
	if err != nil {
		return nil, err
	}
	return []Entry{list, set}, nil
}
