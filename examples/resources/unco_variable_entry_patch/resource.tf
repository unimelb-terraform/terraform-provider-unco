resource "unco_variable_entry_patch" "example" {
  warehouse  = "sample-warehouse"
  set_id     = "acc-sandbox"
  name       = "config"
  patch_json = jsonencode({ id = "123456789012" })
}
