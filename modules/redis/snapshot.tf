# Suffix that makes the final snapshot name unique per replication group
# lifetime.
#
# A fixed "<name>-final" collides when a cache is destroyed, re-created and
# destroyed again (the snapshot already exists). timestamp() would change on
# every plan. random_id is stored in state: it stays the same for the life of
# the replication group, and is created and destroyed together with it, so a
# re-created cache gets a new suffix. The keeper renews it when the ID changes.
resource "random_id" "final_snapshot" {
  count = local.is_development ? 0 : 1

  byte_length = 4

  keepers = {
    replication_group_id = local.full_name
  }
}
