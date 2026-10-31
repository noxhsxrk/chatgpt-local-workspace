# ChatGPT Local Workspace

A small Go MCP server that lets ChatGPT list, read, and write files in a local workspace through OpenAI Secure MCP Tunnel.

## Requirements

- macOS and [Homebrew](https://brew.sh/).
- An OpenAI Platform account with access to [Tunnels](https://platform.openai.com/settings/organization/tunnels).
- A ChatGPT account and workspace that can add custom MCP servers.

## Install with Homebrew

Install and start the app with:

```bash
brew tap noxhsxrk/nox
brew install noxhsxrk/nox/chatgpt-local-workspace
chatgpt-local-workspace
```

If the tap or project repository is private, your GitHub account must have access to both repositories and Git must be configured to authenticate with GitHub.

## Setup

1. In [OpenAI Platform](https://platform.openai.com/settings/organization/tunnels), create a tunnel and associate it with your ChatGPT workspace. Save the tunnel ID.
2. Create a runtime API key with **Tunnels Read + Use** permissions. Keep it private.
3. Start the app. If installed with Homebrew, run `chatgpt-local-workspace`. If using a project checkout, open Terminal in the project directory and run:

   ```bash
   ./start-tunnel.sh
   ```

4. Open the local URL printed in Terminal. Enter the tunnel ID, runtime API key, and an existing workspace directory, then select **Start connection**.

The launcher installs Go and the official OpenAI `tunnel-client` with Homebrew if needed, then starts the local web app. Leave Terminal open; press **Ctrl+C** to stop it. The API key is held in memory while the connection is running and is not saved by the web app. A new tunnel-client profile is created each time you start a connection.

For the interactive terminal flow, run `./start-tunnel.sh --cli` from a project checkout.

## Connect ChatGPT

1. In ChatGPT, open **Plugins** and choose **Add custom MCP server**.
2. Under **Connection**, choose **Tunnel** and select or enter your tunnel ID.
3. Configure authentication if required, then create the plugin.
4. Test it by asking ChatGPT to list the files in your workspace.

For troubleshooting, see the [OpenAI Secure MCP Tunnel guide](https://developers.openai.com/api/docs/guides/secure-mcp-tunnels).

## Notes

- Start with a test folder without sensitive files. The server can create and overwrite files without its own approval prompt.
- The server does not run shell commands, tests, or web servers. It is a prototype, not a production-hardened sandbox.
- Custom MCP access depends on your ChatGPT plan and workspace permissions. The tunnel does not provide unlimited usage.
