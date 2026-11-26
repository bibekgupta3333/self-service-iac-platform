// Test fixtures: trimmed copies of modules/*/module.yaml `inputs` (as the API returns them).

import type { JsonSchema } from "@/lib/api/types";

export const sqsSchema: JsonSchema = {
  $schema: "https://json-schema.org/draft/2020-12/schema",
  type: "object",
  required: ["name"],
  additionalProperties: false,
  properties: {
    name: {
      type: "string",
      pattern: "^[a-z][a-z0-9-]{2,40}$",
      description: "Logical queue name.",
      "x-iacp-immutable": true,
    },
    fifo: { type: "boolean", default: false, description: "Create FIFO queues.", "x-iacp-immutable": true },
    visibility_timeout_seconds: { type: "integer", minimum: 0, maximum: 43200, default: 30 },
    max_receive_count: { type: "integer", minimum: 1, maximum: 100, default: 5, "x-iacp-group": "Dead-letter queue" },
    kms_key_arn: {
      type: ["string", "null"],
      default: null,
      pattern: "^arn:aws[a-z-]*:kms:[a-z0-9-]+:[0-9]{12}:key/.+$",
      "x-iacp-group": "Encryption",
    },
    alarm_actions: {
      type: "array",
      items: { type: "string", pattern: "^arn:" },
      default: [],
      "x-iacp-group": "Alarms",
    },
    tags: {
      type: "object",
      additionalProperties: { type: "string" },
      propertyNames: { not: { pattern: "^iacp:" } },
      maxProperties: 30,
      "x-iacp-ui": "key-value",
    },
  },
};

export const lambdaSchema: JsonSchema = {
  type: "object",
  required: ["name", "runtime", "artifact_s3_uri"],
  properties: {
    name: { type: "string", pattern: "^[a-z][a-z0-9-]{2,40}$" },
    runtime: { type: "string", enum: ["python3.12", "nodejs22.x"], "x-iacp-ui": "select" },
    artifact_s3_uri: { type: "string", "x-iacp-group": "Code" },
    log_retention_days: { type: "integer", enum: [1, 7, 30], default: 30, "x-iacp-group": "Observability" },
    reserved_concurrent_executions: { type: ["integer", "null"], minimum: 1, maximum: 1000, default: 10 },
    sqs_trigger: {
      type: ["object", "null"],
      default: null,
      required: ["queue_arn"],
      properties: { queue_arn: { type: "string" } },
      "x-iacp-group": "Triggers",
    },
  },
};
