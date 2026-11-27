provider "unco" {
  # Optional: falls back to UNCO_API_URL / UNCO_CLIENT_ID / UNCO_TENANT_ID
  # or the Unco CLI config.yaml in the OS user config directory when omitted.
  api_url   = "https://api.example.invalid"
  client_id = "00000000-0000-0000-0000-000000000001"
  tenant_id = "00000000-0000-0000-0000-000000000002"
}
