package toolcatalog

import "github.com/omnara-ai/omnara/internal/toolpermission"

func CustomToolPermissionModes() []toolpermission.ModeDescriptor {
	return toolpermission.CommonModeDescriptors()
}

func DefaultCustomToolPermission() toolpermission.Selection {
	return toolpermission.DefaultSelection(toolpermission.ModeAlwaysAllow)
}

func MCPToolPermissionModes() []toolpermission.ModeDescriptor {
	return toolpermission.CommonModeDescriptors()
}

func DefaultMCPToolPermission() toolpermission.Selection {
	return toolpermission.DefaultSelection(toolpermission.ModeAlwaysAsk)
}
