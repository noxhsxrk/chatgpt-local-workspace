class ChatgptLocalWorkspace < Formula
  desc "Connect a local workspace to ChatGPT through an MCP tunnel"
  homepage "https://github.com/noxhsxrk/chatgpt-local-workspace"
  url "ssh://git@github.com/noxhsxrk/chatgpt-local-workspace.git",
      revision: "3b2aff2e65c6407d5fd4e891dbc29309824ee2ea"
  version "0.1.0"
  revision 4

  depends_on "go" => :build
  depends_on "openai/tools/tunnel-client"

  def install
    system "go", "build", *std_go_args(output: libexec/"workspace-mcp"), "."
    libexec.install "start-tunnel.sh"
    (bin/"chatgpt-local-workspace").write <<~EOS
      #!/bin/bash
      export WORKSPACE_MCP_BINARY="#{libexec}/workspace-mcp"
      exec /bin/bash "#{libexec}/start-tunnel.sh" "$@"
    EOS
    chmod 0755, bin/"chatgpt-local-workspace"
  end

  test do
    ENV["WORKSPACE_ROOT"] = testpath
    request = '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}'
    assert_match "own-mcp-workspace", pipe_output(libexec/"workspace-mcp", request + "\n", 0)
  end
end
