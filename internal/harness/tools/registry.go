package tools

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/omnara-ai/omnara/internal/jsonschema"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
)

type semanticToolInputValidator func(json.RawMessage) error

type toolImplementation struct {
	toolRegistration
	inputSchemaValidator  *jsonschema.Validator
	permissionDescriptors []toolpermission.ModeDescriptor
}

type toolRegistration struct {
	name                   string
	semanticInputValidator semanticToolInputValidator
	handler                toolHandler
	permissionModes        map[string]permissionModeHandler
}

type toolImplementationRegistry struct {
	tools map[string]toolImplementation
}

var loadBuiltInToolImplementations = sync.OnceValues(buildToolImplementationRegistry)

func buildToolImplementationRegistry() (toolImplementationRegistry, error) {
	catalog, err := toolcatalog.Default()
	if err != nil {
		return toolImplementationRegistry{}, err
	}
	for name, descriptors := range map[string][]toolpermission.ModeDescriptor{
		"custom tools": toolcatalog.CustomToolPermissionModes(),
		"MCP tools":    toolcatalog.MCPToolPermissionModes(),
	} {
		if err := validatePermissionModeHandlers(
			commonPermissionModeHandlers(genericPermissionChallenge),
			descriptors,
		); err != nil {
			return toolImplementationRegistry{}, fmt.Errorf("%s permission modes: %w", name, err)
		}
	}
	return newToolImplementationRegistry(catalog, builtInToolRegistrations())
}

// ValidateToolImplementationRegistry builds and validates every tool implementation contract.
func ValidateToolImplementationRegistry() error {
	_, err := loadBuiltInToolImplementations()
	return err
}

func newToolImplementationRegistry(
	catalog toolcatalog.Catalog,
	registrations []toolRegistration,
) (toolImplementationRegistry, error) {
	catalogEntries := catalog.Entries()
	tools := make(map[string]toolImplementation, len(registrations))
	for _, registration := range registrations {
		if registration.name == "" {
			return toolImplementationRegistry{}, errors.New("tool implementation name is required")
		}
		if _, exists := tools[registration.name]; exists {
			return toolImplementationRegistry{}, fmt.Errorf(
				"tool implementation %q is registered more than once",
				registration.name,
			)
		}
		definition, exists := catalog.Lookup(registration.name)
		if !exists {
			return toolImplementationRegistry{}, fmt.Errorf(
				"tool implementation %q is missing from the public catalog",
				registration.name,
			)
		}
		inputSchema, err := jsonschema.Compile(definition.InputSchema)
		if err != nil {
			return toolImplementationRegistry{}, fmt.Errorf(
				"tool implementation %q input schema: %w",
				registration.name,
				err,
			)
		}
		if registration.handler.Transactional == nil && registration.handler.Async == nil {
			return toolImplementationRegistry{}, fmt.Errorf(
				"tool implementation %q requires a transactional or async phase",
				registration.name,
			)
		}
		if err := validatePermissionModeHandlers(
			registration.permissionModes,
			definition.PermissionModes,
		); err != nil {
			return toolImplementationRegistry{}, fmt.Errorf(
				"tool implementation %q permission modes: %w",
				registration.name,
				err,
			)
		}
		tools[registration.name] = toolImplementation{
			toolRegistration:      registration,
			inputSchemaValidator:  inputSchema,
			permissionDescriptors: definition.PermissionModes,
		}
	}
	for _, definition := range catalogEntries {
		if _, exists := tools[definition.Name]; !exists {
			return toolImplementationRegistry{}, fmt.Errorf(
				"public tool %q has no implementation",
				definition.Name,
			)
		}
	}
	return toolImplementationRegistry{tools: tools}, nil
}

func (r toolImplementationRegistry) Lookup(name string) (toolImplementation, bool) {
	implementation, ok := r.tools[name]
	return implementation, ok
}

func toolImplementationFor(name string) (toolImplementation, bool, error) {
	registry, err := loadBuiltInToolImplementations()
	if err != nil {
		return toolImplementation{}, false, err
	}
	if implementation, ok := mcpToolImplementation(name); ok {
		return implementation, true, nil
	}
	implementation, ok := registry.Lookup(name)
	return implementation, ok, nil
}

func mcpToolImplementation(name string) (toolImplementation, bool) {
	if !toolcatalog.IsMCPRuntimeToolName(name) {
		return toolImplementation{}, false
	}
	return toolImplementation{
		toolRegistration: toolRegistration{
			name:            name,
			handler:         toolHandler{Async: runMCPToolAsync},
			permissionModes: commonPermissionModeHandlers(genericPermissionChallenge),
		},
		permissionDescriptors: toolcatalog.MCPToolPermissionModes(),
	}, true
}

func (tool toolImplementation) validateInput(input json.RawMessage) error {
	if tool.inputSchemaValidator != nil {
		if err := tool.inputSchemaValidator.Validate(input); err != nil {
			return fmt.Errorf("tool %q input: %w", tool.name, err)
		}
	}
	if tool.semanticInputValidator != nil {
		return tool.semanticInputValidator(input)
	}
	return nil
}

func builtInToolRegistrations() []toolRegistration {
	return []toolRegistration{
		{
			name:                   toolcatalog.ToolNameRunCommand,
			semanticInputValidator: validateRunCommandInput,
			handler:                toolHandler{Transactional: runCommand, Background: wakeProcessTool},
			permissionModes:        commonPermissionModeHandlers(runCommandPermissionChallenge),
		},
		{
			name:                   toolcatalog.ToolNameUploadArtifact,
			semanticInputValidator: validateUploadArtifactInput,
			handler:                toolHandler{Transactional: runUploadArtifact, Background: wakeProcessTool},
			permissionModes:        commonPermissionModeHandlers(uploadArtifactPermissionChallenge),
		},
		{
			name:                   toolcatalog.ToolNameDownloadArtifact,
			semanticInputValidator: validateDownloadArtifactInput,
			handler:                toolHandler{Transactional: runDownloadArtifact, Background: wakeProcessTool},
			permissionModes:        commonPermissionModeHandlers(downloadArtifactPermissionChallenge),
		},
		{
			name:                   toolcatalog.ToolNameWriteProcess,
			semanticInputValidator: validateWriteProcessInput,
			handler:                toolHandler{Transactional: writeProcess},
			permissionModes:        commonPermissionModeHandlers(writeProcessPermissionChallenge),
		},
		{
			name:                   toolcatalog.ToolNameReadProcess,
			semanticInputValidator: processObservationInputValidator(processObservationRead),
			handler: toolHandler{
				Transactional: processObservation(processObservationRead),
				Background:    wakeReadProcess,
			},
			permissionModes: commonPermissionModeHandlers(
				processObservationPermissionChallenge(processObservationRead),
			),
		},
		{
			name:                   toolcatalog.ToolNameStopProcess,
			semanticInputValidator: validateStopProcessInput,
			handler:                toolHandler{Transactional: stopProcess},
			permissionModes:        commonPermissionModeHandlers(stopProcessPermissionChallenge),
		},
		{
			name:                   toolcatalog.ToolNameListProcesses,
			semanticInputValidator: processObservationInputValidator(processObservationList),
			handler:                toolHandler{Transactional: processObservation(processObservationList)},
			permissionModes: commonPermissionModeHandlers(
				processObservationPermissionChallenge(processObservationList),
			),
		},
		{
			name:                   toolcatalog.ToolNameCreateMachine,
			semanticInputValidator: validateCreateMachineInput,
			handler: toolHandler{
				Transactional: createMachine,
				Background:    provisionMachineInBackground,
			},
			permissionModes: commonPermissionModeHandlers(createMachinePermissionChallenge),
		},
		{
			name:                   toolcatalog.ToolNameDeleteMachine,
			semanticInputValidator: validateDeleteMachineInput,
			handler: toolHandler{
				Transactional: deleteMachine,
				Background:    deleteMachineInBackground,
			},
			permissionModes: commonPermissionModeHandlers(genericPermissionChallenge),
		},
		{
			name:                   toolcatalog.ToolNameListMachines,
			semanticInputValidator: validateListMachinesInput,
			handler:                toolHandler{Transactional: listMachines},
			permissionModes:        commonPermissionModeHandlers(listMachinesPermissionChallenge),
		},
		{
			name:                   toolcatalog.ToolNameInspectMachine,
			semanticInputValidator: validateInspectMachineInput,
			handler:                toolHandler{Transactional: inspectMachine},
			permissionModes:        commonPermissionModeHandlers(inspectMachinePermissionChallenge),
		},
		{
			name:                   toolcatalog.ToolNameAskQuestion,
			semanticInputValidator: validateQuestionInput,
			handler: toolHandler{
				Transactional: prepareStructuredQuestion,
				Async:         deliverStructuredQuestionPrompts,
			},
			permissionModes: commonPermissionModeHandlers(genericPermissionChallenge),
		},
		{
			name:                   toolcatalog.ToolNameSendIntegrationMessage,
			semanticInputValidator: validateIntegrationMessageInput,
			handler:                toolHandler{Async: runIntegrationMessageAsync},
			permissionModes:        alwaysAllowPermissionModeHandlers(),
		},
		{
			name:                   toolcatalog.ToolNameSetIntegrationTarget,
			semanticInputValidator: validateIntegrationTargetInput,
			handler:                toolHandler{Transactional: setIntegrationTarget},
			permissionModes: commonPermissionModeHandlers(
				setIntegrationTargetPermissionChallenge,
			),
		},
		{
			name: toolcatalog.ToolNameSendChannelMessage,
			semanticInputValidator: func(input json.RawMessage) error {
				_, err := resolveChannelMessageRequest(input)
				return err
			},
			handler:         toolHandler{Async: runChannelMessageAsync},
			permissionModes: alwaysAllowPermissionModeHandlers(),
		},
		{
			name: toolcatalog.ToolNameListChannels,
			semanticInputValidator: func(input json.RawMessage) error {
				_, _, err := resolveChannelListRequest(input)
				return err
			},
			handler:         toolHandler{Transactional: listChannels},
			permissionModes: commonPermissionModeHandlers(listChannelsPermissionChallenge),
		},
		{
			name:                   toolcatalog.ToolNameWebSearch,
			semanticInputValidator: validateWebSearchInput,
			handler:                toolHandler{Async: runWebSearch},
			permissionModes:        commonPermissionModeHandlers(genericPermissionChallenge),
		},
		{
			name:                   toolcatalog.ToolNameWebFetch,
			semanticInputValidator: validateWebFetchInput,
			handler:                toolHandler{Async: runWebFetch},
			permissionModes:        commonPermissionModeHandlers(genericPermissionChallenge),
		},
		{
			name:                   toolcatalog.ToolNameSkill,
			semanticInputValidator: validateSkillInput,
			handler:                toolHandler{Async: runSkillTool},
			permissionModes:        commonPermissionModeHandlers(genericPermissionChallenge),
		},
	}
}

func validateWriteProcessInput(input json.RawMessage) error {
	_, _, err := writeProcessPayload(input)
	return err
}

func processObservationInputValidator(
	mode processObservationMode,
) semanticToolInputValidator {
	return func(input json.RawMessage) error {
		_, err := resolveProcessObservationRequest(input, mode)
		return err
	}
}

func validateStopProcessInput(input json.RawMessage) error {
	_, _, err := resolveStopProcessRequest(input)
	return err
}

func validatePermissionModeHandlers(
	handlers map[string]permissionModeHandler,
	descriptors []toolpermission.ModeDescriptor,
) error {
	if len(handlers) != len(descriptors) {
		return fmt.Errorf(
			"handler count %d does not match catalog mode count %d",
			len(handlers),
			len(descriptors),
		)
	}
	for _, descriptor := range descriptors {
		if handlers[descriptor.Name] == nil {
			return fmt.Errorf("mode %q has no handler", descriptor.Name)
		}
	}
	return nil
}
