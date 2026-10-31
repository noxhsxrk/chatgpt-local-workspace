# Local agent capabilities

The current server exposes workspace list, literal text search, read, and write tools, plus tools to list and read locally installed skills and agent instruction profiles. ChatGPT supplies the model through the existing tunnel. This application does not currently run an independent model or support arbitrary shell commands.

## Reference and boundaries

[DWB MCP Studio](https://github.com/sphakanin/dwb-mcp-studio-public) combines a multi-worker MCP broker with workspace selection and a managed skill registry. Its skill policies distinguish automatic use, user approval, and explicit invocation. DWB is MIT-licensed; no DWB source code has been incorporated here.

## Work needed for an agent-style experience

1. **MCP runtime:** use the official MCP Go SDK to support standard transports, tool discovery, cancellation, protocol negotiation, and multiple server connections. Isolate configuration and credentials per connection, and show connection and initialization errors.
2. **Workspace and process management:** provide explicit workspace selection, scoped process execution, per-action approval, timeouts, output limits, lifecycle cleanup, and protection against concurrent edits. Resolve the existing filesystem race limitation before treating workspace containment as a security boundary.
3. **Skills:** the app can install and remove trusted local `SKILL.md` folders and expose their content through MCP. Add explicitly requested GitHub installation, complete [Agent Skills](https://agentskills.io/specification) format validation, updates, and per-workspace AUTO / ASK / MANUAL policies. Instructions in a skill do not grant system permissions.
4. **Agents:** user-created instruction profiles can be listed and read through MCP. Add explicit configuration of allowed tools, workspace and skills. A profile does not start an independent local model or independently create ChatGPT subagents.
5. **Plugins:** choose a compatible manifest and lifecycle before installing third-party code. Show source and requested capabilities, require explicit installation, and distinguish MCP servers from executable plugins and instruction-only skills.
6. **Tests:** use controlled MCP test servers and local temporary workspaces to verify initialization, cancellation, per-session isolation, reconnection, malformed packages, permissions and tool behavior before trying a real account.

External MCP connections, plugin installation, automatic skill policies, and independent agent execution are not implemented yet. Exact plugin compatibility needs to be chosen before its dependent implementation.
