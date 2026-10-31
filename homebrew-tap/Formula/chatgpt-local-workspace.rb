class ChatgptLocalWorkspace < Formula
  desc "Connect a local workspace to ChatGPT through an MCP tunnel"
  homepage "https://github.com/noxhsxrk/chatgpt-local-workspace"
  url "https://github.com/noxhsxrk/chatgpt-local-workspace.git",
      revision: "8d0300431c3fd376bc8a045bb963bd737ec5f46f"
  version "0.1.0"

  depends_on "go" => :build
  depends_on "openai/tools/tunnel-client"

  def install
    system "go", "build", *std_go_args(output: libexec/"workspace-mcp"), "."
    libexec.install "start-tunnel.sh"
    bin.install_env_script libexec/"start-tunnel.sh", WORKSPACE_MCP_BINARY: libexec/"workspace-mcp"
    mv bin/"start-tunnel.sh", bin/"chatgpt-local-workspace"
  end

  test do
    ENV["WORKSPACE_ROOT"] = testpath
    request = '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}'
    assert_match "own-mcp-workspace", pipe_output(libexec/"workspace-mcp", request + "\n", 0)
  end
end
