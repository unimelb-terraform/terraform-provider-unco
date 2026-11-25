resource "unco_variable_entry" "example" {
  warehouse = "sample-warehouse"
  set_id    = "shared-outputs"
  name      = "EndpointURL"
  kind      = "string"
  target    = "unexported"
  value     = "https://events.example.invalid/queue"
}
