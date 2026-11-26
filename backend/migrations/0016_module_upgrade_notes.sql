-- Upgrade notes per module version (WBS 8.2, docs/04 §5.2, docs/06 §1).
--
-- module-release.yml sends the "### Migration" (or "### Upgrade notes") subsection of the
-- version's CHANGELOG section, or module.yaml upgrade.notes, as upgrade_notes when it
-- indexes a release. The upgrade-path API and the portal's major-upgrade panel show them.
-- Versions indexed before this column existed keep NULL; the API then derives the notes
-- from changelog_md. Versions are immutable once indexed, so nothing updates the column.
-- iacp_app keeps the table's existing grants (0004).

-- +goose Up
ALTER TABLE module_versions ADD COLUMN upgrade_notes text;

-- +goose Down
ALTER TABLE module_versions DROP COLUMN upgrade_notes;
