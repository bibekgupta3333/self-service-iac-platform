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

  lifecycle {
    postcondition {
      condition     = length(var.availability_zones) > 0 || length(self.names) >= var.az_count
      error_message = "az_count is ${var.az_count}, but region ${data.aws_region.current.name} has only ${length(self.names)} available zones (${join(", ", sort(self.names))}). Lower az_count or list availability_zones."
    }

    postcondition {
      condition     = alltrue([for az in var.availability_zones : contains(self.names, az)])
      error_message = "availability_zones lists zones that are not available in region ${data.aws_region.current.name} (available: ${join(", ", sort(self.names))})."
    }
  }
}
