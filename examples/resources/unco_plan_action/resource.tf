resource "unco_plan_action" "example" {
  warehouse     = "sample-warehouse"
  stack         = "customs-sandbox"
  delay_seconds = 60

  triggers = {
    account_id = "123456789012"
  }
}
