variable "docs_mcp_token" {
  type      = string
  sensitive = true
}

# A remote MCP server authenticated with a static header. The header values are
# write-only: orq never returns them, so they live only in state.
resource "orq_mcp_server" "docs" {
  key          = "docs"
  display_name = "Docs MCP"
  description  = "Search and read the product documentation"

  upstream = {
    type = "HTTP" # HTTP | SSE | OPENAPI
    url  = "https://mcp.example.com/mcp"
  }

  auth = {
    type = "STATIC_HEADERS"
    static_headers = {
      Authorization = "Bearer ${var.docs_mcp_token}"
    }
  }

  # Fallback for gateway links that do not narrow the tool surface themselves.
  default_tool_exposure = {
    mode      = "ALL" # ALL | SELECTED | NONE
    read_only = true
  }

  sharing = {
    all_projects = true
  }
}

# OAuth client credentials — set `oauth` instead of `static_headers`:
#
# resource "orq_mcp_server" "crm" {
#   key          = "crm"
#   display_name = "CRM MCP"
#
#   upstream = {
#     type = "HTTP"
#     url  = "https://crm.example.com/mcp"
#   }
#
#   auth = {
#     type = "OAUTH_CLIENT_CREDENTIALS"
#     oauth = {
#       client_id     = "orq-gateway"
#       client_secret = var.crm_client_secret
#       token_url     = "https://auth.example.com/oauth/token"
#       scopes        = ["crm.read"]
#     }
#   }
#
#   default_tool_exposure = {
#     mode = "ALL"
#   }
# }

# An OpenAPI document served as MCP tools; `upstream.url` is the API base URL.
#
# resource "orq_mcp_server" "petstore" {
#   key          = "petstore"
#   display_name = "Petstore API"
#
#   upstream = {
#     type = "OPENAPI"
#     url  = "https://petstore.example.com/v1"
#   }
#
#   auth = {
#     type = "NONE"
#   }
#
#   openapi = {
#     document_url = "https://petstore.example.com/openapi.json"
#     operations   = ["listPets", "getPetById"]
#   }
#
#   default_tool_exposure = {
#     mode = "ALL"
#   }
# }
