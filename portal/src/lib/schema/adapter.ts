// SchemaAdapter binds the pure schema functions to one module version, so components ask
// it for fields, groups and inputs instead of threading the schema around.

import type { ModuleVersion } from "@/lib/api/types";

import { type FieldGroup, type FieldModel, groupFields, toFieldModels } from "./fields";
import { type FieldValue, type FormValues, type InputsResult, initialValues, toInputs } from "./values";

export class SchemaAdapter {
  readonly module: string;
  readonly version: string;
  readonly fields: readonly FieldModel[];
  private readonly byName: ReadonlyMap<string, FieldModel>;

  constructor(version: Pick<ModuleVersion, "module" | "version" | "inputs_schema">) {
    this.module = version.module;
    this.version = version.version;
    this.fields = toFieldModels(version.inputs_schema);
    this.byName = new Map(this.fields.map((f) => [f.name, f]));
  }

  groups(): FieldGroup[] {
    return groupFields(this.fields);
  }

  field(name: string): FieldModel | undefined {
    return this.byName.get(name);
  }

  initialValues(): Record<string, FieldValue> {
    return initialValues(this.fields);
  }

  toInputs(values: FormValues): InputsResult {
    return toInputs(this.fields, values);
  }

  /** A default resource name for the module: a valid HCL identifier (^[a-z][a-z0-9_]{0,62}$). */
  defaultResourceName(): string {
    const id = this.module.toLowerCase().replace(/[^a-z0-9_]+/g, "_").replace(/^[^a-z]+/, "");
    return (id || "resource").slice(0, 63);
  }
}
