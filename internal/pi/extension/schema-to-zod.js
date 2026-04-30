import { z } from "zod";

export function schemaToZod(schema) {
  if (!schema || typeof schema !== "object") {
    return z.unknown();
  }

  if (Array.isArray(schema.anyOf)) {
    const variants = schema.anyOf.map(schemaToZod);
    if (variants.length === 0) {
      return z.unknown();
    }
    if (variants.length === 1) {
      return variants[0];
    }
    return z.union(variants);
  }

  if (Array.isArray(schema.enum)) {
    const values = schema.enum;
    if (values.length === 0) {
      return z.never();
    }
    if (values.length === 1) {
      return z.literal(values[0]);
    }
    if (values.every((value) => typeof value === "string")) {
      return z.enum(values);
    }
    return z.union(values.map((value) => z.literal(value)));
  }

  if (Object.hasOwn(schema, "const")) {
    return z.literal(schema.const);
  }

  switch (schema.type) {
    case "object":
      return objectToZod(schema);
    case "array": {
      let result = z.array(schemaToZod(schema.items));
      if (Number.isInteger(schema.minItems)) result = result.min(schema.minItems);
      if (Number.isInteger(schema.maxItems)) result = result.max(schema.maxItems);
      return result;
    }
    case "string": {
      let result = z.string();
      if (Number.isInteger(schema.minLength)) result = result.min(schema.minLength);
      if (Number.isInteger(schema.maxLength)) result = result.max(schema.maxLength);
      if (schema.description) result = result.describe(schema.description);
      return result;
    }
    case "boolean":
      return z.boolean();
    case "integer":
      return z.number().int();
    case "number":
      return z.number();
    case "null":
      return z.null();
    default:
      return z.unknown();
  }
}

function objectToZod(schema) {
  const required = new Set(schema.required ?? []);
  const shape = {};

  for (const [key, value] of Object.entries(schema.properties ?? {})) {
    const converted = schemaToZod(value);
    shape[key] = required.has(key) ? converted : converted.optional();
  }

  let result = z.object(shape);
  if (schema.description) result = result.describe(schema.description);
  return result;
}
