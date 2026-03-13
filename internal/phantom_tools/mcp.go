// This file is a placeholder for future MCP server integration.
//
// The proxy can connect to MCP servers, fetch their tools, and inject them as phantom tools.
// The client never sees these tools -- they're handled entirely by the proxy's phantom loop.
// This enables the proxy to provide additional capabilities (database access, search, monitoring)
// without modifying any client agent.
//
// DESIGN:
//  1. On startup (or config reload), the gateway connects to configured MCP servers.
//  2. It calls tools/list to discover available tools.
//  3. Each MCP tool is wrapped as a PhantomTool and registered in the global registry.
//  4. When the LLM calls an MCP phantom tool, the phantom loop routes the call
//     to the appropriate MCP server via tools/call.
//  5. The result is returned to the LLM as a normal tool_result.
//
// This means any MCP-compatible server (database, monitoring, search, etc.)
// can be transparently provided to the LLM without the client knowing.
package phantom_tools

import "errors"

// ErrNotImplemented is returned by placeholder MCP functions.
var ErrNotImplemented = errors.New("MCP phantom tool integration is not yet implemented")

// MCPPhantomTool represents a phantom tool backed by an MCP server.
type MCPPhantomTool struct {
	// ServerURL is the MCP server endpoint (e.g., "http://localhost:3000/mcp").
	ServerURL string

	// ToolName is the tool name as reported by the MCP server's tools/list.
	ToolName string

	// DisplayName is the name exposed to the LLM (may differ from ToolName).
	// If empty, ToolName is used.
	DisplayName string

	// Description overrides the MCP tool's description if set.
	Description string

	// Handler is an optional custom handler for processing tool results.
	// If nil, the raw MCP response is returned to the LLM.
	Handler func(input map[string]any) (map[string]any, error)
}

// RegisterMCPTool registers an MCP-backed phantom tool.
// This is a placeholder -- the actual implementation will:
//  1. Connect to the MCP server
//  2. Call tools/list to get the tool schema
//  3. Pre-compute JSON bytes for each provider format
//  4. Register the tool in the global phantom registry
//
// Returns ErrNotImplemented until MCP integration is built.
func RegisterMCPTool(tool MCPPhantomTool) error {
	return ErrNotImplemented
}

// DiscoverMCPTools connects to an MCP server and registers all its tools as phantom tools.
// This is a placeholder for the full discovery flow.
//
// Returns ErrNotImplemented until MCP integration is built.
func DiscoverMCPTools(serverURL string) error {
	return ErrNotImplemented
}
