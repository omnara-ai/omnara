package apimcp

import "regexp"

// The API describes IDs as "Project ID (proj_...).", which only repeats the
// schema pattern. MCP clients get where to find the ID instead.
var idDescriptions = map[string]string{
	"acfg": "Agent config ID, from configs_create or current_config_id on an agent or profile.",
	"agt":  "Agent ID, from agents_list or agents_launch.",
	"aprf": "Agent profile ID, from profiles_list.",
	"cron": "Cron trigger ID, from crons_list.",
	"int":  "Interaction ID, from agents_interactions_list.",
	"mch":  "Machine ID, from machines_list.",
	"mdl":  "Configured model ID, from models_list.",
	"mpc":  "Model provider ID, from model_providers_list.",
	"mpo":  "Machine pool ID, from pools_list.",
	"org":  "Organization ID, from orgs_list or whoami.",
	"pmg":  "Project machine grant ID, from grant_machines_list.",
	"pmog": "Project model grant ID, from grant_models_list.",
	"pmpg": "Project pool grant ID, from grant_pools_list.",
	"proj": "Project ID, from projects_list.",
	"sec":  "Secret ID, from secrets_list.",
	"sgr":  "Secret grant ID, from grant_secrets_list.",
	"skg":  "Skill grant ID, from grant_skills_list.",
	"skl":  "Skill ID, from skills_list.",
}

var apiIDDescription = regexp.MustCompile(`^.+ ID \(([a-z]+)_\.\.\.\)\.$`)

func describeIDs(value any) {
	switch typed := value.(type) {
	case map[string]any:
		if description, ok := typed["description"].(string); ok {
			if match := apiIDDescription.FindStringSubmatch(description); match != nil {
				if replacement, ok := idDescriptions[match[1]]; ok {
					typed["description"] = replacement
				}
			}
		}
		for _, child := range typed {
			describeIDs(child)
		}
	case []any:
		for _, item := range typed {
			describeIDs(item)
		}
	}
}
