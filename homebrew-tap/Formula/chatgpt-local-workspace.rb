class ChatgptLocalWorkspace < Formula
  desc "Connect a local workspace to ChatGPT through an MCP tunnel"
  homepage "https://github.com/noxhsxrk/chatgpt-local-workspace"
  url "https://github.com/noxhsxrk/chatgpt-local-workspace/archive/refs/tags/v0.2.6.tar.gz"
  version "0.2.6"
  sha256 "41fdcf961e66ef3ea7fcfb650644d7f9a28b6141b9b4434ac41cadaebbd98e51"
  license all_of: ["MIT", "Apache-2.0", "BSD-3-Clause"]

  depends_on "go" => :build
  depends_on "openai/tools/tunnel-client"

  def fetch
    system "go", "mod", "download"
  end

  def install
    ENV["GOPROXY"] = "off"
    system "go", "build", *std_go_args(output: libexec/"workspace-mcp"), "./cmd/workspace"
    libexec.install "start-tunnel.sh"
    pkgshare.install "third_party_licenses"
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
