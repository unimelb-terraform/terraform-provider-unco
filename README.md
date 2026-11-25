# terraform-provider-unco

Terraform provider (`registry.terraform.io/unimelb-terraform/unco`) for managing Unco v2 variable set entries and triggering delayed stack plans.

## Configuration & Authentication

Configuration values (`api_url`, `client_id`, `tenant_id`, and optional `token`) are resolved in order from:

1. Explicit provider attributes (`api_url`, `client_id`, `tenant_id`, `token`),
2. Environment variables (`UNCO_API_URL`, `UNCO_CLIENT_ID`, `UNCO_TENANT_ID`, `UNCO_API_TOKEN`),
3. `~/.config/unco/config.yaml` (`api.url`, `api.clientId`, `api.tenantId`).

When `token` / `UNCO_API_TOKEN` is omitted, the provider acquires a bearer token from the local Unco MSAL token cache (`~/.cache/unco/cache.json`).

## Resources

- `unco_variable_entry` — Declarative CRUD + drift detection + `terraform import <warehouse>/<set_id>/<name>` for a single variable entry inside a variable set (serializing concurrent writes to the same set via an in-process mutex and `version` conflict retries while preserving sibling entries).
- `unco_variable_entry_patch` — Imperative apply-time RFC 7396 JSON merge-patch onto an existing JSON-valued variable entry.
- `unco_variable_entry_delete` — Imperative idempotent apply-time removal of a variable entry.
- `unco_plan_action` — Imperative apply-time remote `plan` trigger (`POST /warehouses/{warehouse}/stacks/{stack}/actions/plan`) with optional `delay_seconds` (`0..900`).
