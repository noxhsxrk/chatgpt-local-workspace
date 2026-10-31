class ChatgptLocalWorkspace < Formula
  desc "Connect a local workspace to ChatGPT through an MCP tunnel"
  homepage "https://github.com/noxhsxrk/chatgpt-local-workspace"
  url "https://github.com/noxhsxrk/chatgpt-local-workspace/archive/refs/tags/v0.2.3.tar.gz"
  version "0.2.3"
  sha256 "41b3a0ea95dcdcec74aaf72c00c97c6b55458fcbd2db4a5591f962308fac7cd7"
  license "MIT"

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
    response = pipe_output(libexec/"workspace-mcp", request + "\n", 0)
    assert_match "own-mcp-workspace", response
    assert_match version.to_s, response
  end
end
