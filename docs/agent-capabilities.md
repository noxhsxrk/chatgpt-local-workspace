# Local agent capabilities

The current server exposes workspace list, literal text search, read, and write tools, plus tools to list and read locally installed skills and agent instruction profiles. ChatGPT supplies the model through the existing tunnel. This application does not currently run an independent model or support arbitrary shell commands.

## Reference and boundaries

[DWB MCP Studio](https://github.com/sphakanin/dwb-mcp-studio-public) combines a multi-worker MCP broker with workspace selection and a managed skill registry. Its skill policies distinguish automatic use, user approval, and explicit invocation. DWB is MIT-licensed; no DWB source code has been incorporated here.

## Work needed for an agent-style experience

1. **MCP runtime:** stdio and Streamable HTTP external servers are available through the official MCP Go SDK using `mcp_servers_list`, `mcp_server_tools`, and `mcp_server_call`. Add direct tool aggregation, richer status and cancellation, OAuth, and per-workspace access policies. External servers currently apply to every tunnel.
2. **Workspace and process management:** provide explicit workspace selection, scoped process execution, per-action approval, timeouts, output limits, lifecycle cleanup, and protection against concurrent edits. Resolve the existing filesystem race limitation before treating workspace containment as a security boundary.
3. **Skills:** the app can install and remove local `SKILL.md` folders or download skills from public GitHub repositories found on skills.sh, and expose their content through MCP. Add complete [Agent Skills](https://agentskills.io/specification) format validation, updates, and per-workspace AUTO / ASK / MANUAL policies. Instructions in a skill do not grant system permissions.
4. **Agents:** user-created instruction profiles can be listed and read through MCP. Add explicit configuration of allowed tools, workspace and skills. A profile does not start an independent local model or independently create ChatGPT subagents.
5. **Plugins:** choose a compatible manifest and lifecycle before installing third-party code. Show source and requested capabilities, require explicit installation, and distinguish MCP servers from executable plugins and instruction-only skills.
6. **Tests:** use controlled MCP test servers and local temporary workspaces to verify initialization, cancellation, per-session isolation, reconnection, malformed packages, permissions and tool behavior before trying a real account.

General plugin installation, automatic skill policies, direct tool aggregation, and independent agent execution are not implemented yet. Exact plugin compatibility needs to be chosen before its dependent implementation.
