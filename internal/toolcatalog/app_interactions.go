package toolcatalog

const (
	ToolNameListInteractionDestinations = "list_interaction_destinations"
	ToolNameSetInteractionDestination   = "set_interaction_destination"
)

func InteractionDestinationToolNames() []string {
	return []string{ToolNameListInteractionDestinations, ToolNameSetInteractionDestination}
}

func IsInteractionDestinationTool(name string) bool {
	return name == ToolNameListInteractionDestinations || name == ToolNameSetInteractionDestination
}

func interactionDestinationTools() ([]Entry, error) {
	list, err := toolEntry(ToolNameListInteractionDestinations,
		"List eligible destinations for future questions and approval prompts, including resource keys, "+
			"connection IDs and concrete conversation scopes. The current selection is null for dashboard only. "+
			"Ordinary message tools choose their own destinations.", nil, nil)
	if err != nil {
		return nil, err
	}
	set, err := toolEntry(ToolNameSetInteractionDestination,
		"Choose where future questions and approval prompts are mirrored. Copy a target_id and resource "+
			"from list_interaction_destinations, or pass destination: null for dashboard only. "+
			"This does not move existing prompts or change ordinary message destinations. "+
			"The dashboard remains available if mirroring fails.",
		[]string{"destination"}, map[string]any{
			"destination": map[string]any{
				"anyOf": []any{
					map[string]any{"type": "null"},
					objectSchema(map[string]any{
						"target_id": map[string]any{
							"type": "string", "pattern": `^itgt_[a-z2-7]{26}$`,
							"description": "Public target_id returned by list_interaction_destinations.",
						},
						"resource": map[string]any{
							"type": "string", "pattern": ToolNamePattern,
							"description": "Interaction handler resource key returned by list_interaction_destinations.",
						},
					}, []string{"target_id", "resource"}),
				},
			},
		})
	if err != nil {
		return nil, err
	}
	return []Entry{list, set}, nil
}
