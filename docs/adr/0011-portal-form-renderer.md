# ADR-0011: Portal request form uses a small typed schema renderer instead of `@rjsf/core`

- Status: Proposed
- Date: 2026-11-26
- Deciders: Platform team
- Related: [04-module-registry.md](../04-module-registry.md) (`module.yaml` inputs), [03-data-model-and-api.md §6.4](../03-data-model-and-api.md#64-requests), [08-mvp-roadmap.md](../08-mvp-roadmap.md) (M4), [portal/README.md](../../portal/README.md)

## Context

WBS 7.3 and docs/08 planned the portal's request form on `@rjsf/core` with
`@rjsf/validator-ajv8`. When the portal was built:

- rjsf 6 supports React 19, so compatibility was not a blocker.
- Module input schemas (`module.yaml` `inputs`) are JSON Schema 2020-12 and flat:
  - top-level scalars, enums, string lists and string maps;
  - `type: [T, "null"]` unions;
  - the platform's `x-iacp-group`, `x-iacp-immutable` and `x-iacp-ui` hints.
- A few inputs are richer: nested objects, arrays of objects, and `if`/`then`/`allOf`.
- The backend validates every request against the full schema (`requests.ValidateResources`)
  and reports violations as RFC 6901 pointers such as `/resources/0/inputs/name`.
  The client check is a convenience, not the authority.
- The catalog page (WBS 7.2) needs an inputs table, so a schema-to-field model is needed
  anyway.
- rjsf's default validator is draft-07. Using it here would need `Ajv2020`, a custom-keyword
  setup for `x-iacp-*`, and theme work to match the portal, and it adds a large client bundle.

## Decision

The portal renders the form itself from a pure, typed field model:

1. **Field model.** `toFieldModels(schema)` (`portal/src/lib/schema/fields.ts`) turns each
   top-level property into a `FieldModel`:
   - a control kind: string, integer, number, boolean, enum, string list, string map, or a JSON
     fallback for anything richer;
   - its constraints, default, group and hints.
   The same model drives the 7.2 inputs table.
2. **Values.** `toInputs` (`values.ts`) parses control values, runs light checks (required,
   pattern, length, inclusive and exclusive bounds, enum) and builds the `inputs` object. It
   sends only required values and values that differ from the default. `clearBehaviour` defines
   what an emptied control means: null, an explicit empty value the schema allows, or the module
   default.
3. **Backend errors.** These are mapped onto controls by pointer (`mapFieldErrors`), including
   missing required inputs reported at the inputs object itself.
4. **Components.** `SchemaAdapter` binds these functions to one module version. React
   components only render it.

## Consequences

Positive:
- One small model serves both the catalog table and the form, and is unit-tested without a DOM.
- Backend pointers map one-to-one onto controls, so server errors land next to the right field.
- No extra validator, theme or bundle weight; the backend stays the single schema validator.

Negative:
- Nested objects, arrays of objects and conditionals are edited as JSON rather than as nested
  controls. If module schemas grow deeper, the renderer must grow with them, or this decision
  should be revisited.
- The client checks cover only part of JSON Schema. Anything else is caught by the backend on
  submit.

## Alternatives considered

- **`@rjsf/core` with `@rjsf/validator-ajv8` (`Ajv2020`).** It renders nested schemas out of the
  box. It was rejected for now because of the validator and theme setup, the bundle size, a
  second error-mapping path for backend pointers, and the separate model still needed for the
  inputs table.
- **JSON-only editor.** This was rejected because it gives poor guidance for the common flat
  inputs.
