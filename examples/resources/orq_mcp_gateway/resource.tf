# A gateway that exposes the read-only tools of one upstream server to two
# projects. A server can only be linked after its tools were synced once.
resource "orq_mcp_gateway" "support" {
  key          = "support"
  display_name = "Support tools"
  description  = "Tools available to the support agents"
  mode         = "DIRECT"              # CODE | DIRECT
  tool_naming  = "PREFIX_ON_COLLISION" # PREFIX_WITH_SERVER_KEY | PREFIX_ON_COLLISION

  server_links = [
    {
      mcp_server_id = orq_mcp_server.docs.id
      alias         = "docs"
      enabled       = true

      tool_exposure = {
        mode      = "ALL" # ALL | SELECTED | NONE
        read_only = true
      }
    },
  ]

  # Must stay within the sharing of every linked server.
  sharing = {
    project_ids = [orq_project.support.id, orq_project.sales.id]
  }

  guardrails {
    id           = "orq_secret_detection"
    execute_on   = "both" # input | output | both
    is_guardrail = true
  }
}

output "support_gateway_url" {
  value = orq_mcp_gateway.support.public_url
}
