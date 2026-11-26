data "aws_caller_identity" "current" {}

data "aws_partition" "current" {}

data "aws_region" "current" {}

data "aws_availability_zones" "available" {
  #checkov:skip=CKV_AWS_394:Only the first az_count names of the sorted list are used, so a newly added zone (AWS appends letters) does not change the selection; callers who need a closed set pin availability_zones.
  state = "available"

  # Local Zones and Wavelength Zones are opt-in and lack most services.
  filter {
    name   = "opt-in-status"
    values = ["opt-in-not-required"]
  }
}
