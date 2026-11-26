import { render, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { groupFields, toFieldModels } from "@/lib/schema/fields";
import { sqsSchema } from "@/lib/schema/fixtures";

import { InputsTable } from "./InputsTable";

describe("InputsTable", () => {
  it("renders one row per input with group headers", () => {
    render(<InputsTable groups={groupFields(toFieldModels(sqsSchema))} />);
    const table = screen.getByRole("table", { name: "Module inputs" });
    expect(within(table).getByRole("columnheader", { name: "Encryption" })).toBeInTheDocument();
    const nameRow = within(table).getByRole("rowheader", { name: /^name/ }).closest("tr");
    expect(nameRow).not.toBeNull();
    expect(within(nameRow!).getByText("(required)")).toBeInTheDocument();
    expect(within(nameRow!).getByText("immutable after create")).toBeInTheDocument();
    const kmsRow = within(table).getByRole("rowheader", { name: "kms_key_arn" }).closest("tr");
    expect(within(kmsRow!).getByText("string | null")).toBeInTheDocument();
  });

  it("says so when there are no inputs", () => {
    render(<InputsTable groups={[]} />);
    expect(screen.getByText("This version declares no inputs.")).toBeInTheDocument();
  });
});
