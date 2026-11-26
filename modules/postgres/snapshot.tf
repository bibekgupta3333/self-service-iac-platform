# Suffix that makes the final snapshot name unique per instance lifetime.
#
# A fixed "<name>-final" collides when an instance is destroyed, re-created
# and destroyed again (DBSnapshotAlreadyExists). timestamp() would change on
# every plan. random_id is stored in state: it stays the same for the life of
# the instance, and is created and destroyed together with it, so a re-created
# instance gets a new suffix. The keeper renews it when the identifier changes.
resource "random_id" "final_snapshot" {
  count = local.skip_final_snapshot ? 0 : 1

  byte_length = 4

  keepers = {
    identifier = local.full_name
  }
}
