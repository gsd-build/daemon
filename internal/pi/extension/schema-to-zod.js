import { z } from "zod";

const NO_LITERAL = Symbol("no-literal");

export function schemaToZod(schema) {
  if (!schema || typeof schema !== "object") {
    return z.unknown();
  }

  if (Array.isArray(schema.anyOf)) {
    const variants = schema.anyOf.map(schemaToZod);
    const discriminator = discriminatorForAnyOf(schema.anyOf);
    if (variants.length === 0) {
      return z.unknown();
    }
    if (variants.length === 1) {
      return variants[0];
    }
    if (discriminator && variants.every((variant) => variant instanceof z.ZodObject)) {
      return z.discriminatedUnion(discriminator, variants);
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
      if (schema.format === "uuid") {
        result = result.uuid(schema.errorMessage ?? "Invalid UUID");
      }
      if (schema.description) result = result.describe(schema.description);
      return result;
    }
    case "boolean":
      return z.boolean();
    case "integer":
      return applyNumberBounds(z.number().int(), schema);
    case "number":
      return applyNumberBounds(z.number(), schema);
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

function applyNumberBounds(result, schema) {
  let bounded = result;
  if (typeof schema.minimum === "number") bounded = bounded.min(schema.minimum);
  if (typeof schema.maximum === "number") bounded = bounded.max(schema.maximum);
  return bounded;
}

function discriminatorForAnyOf(variants) {
  const first = variants[0];
  if (!first || first.type !== "object" || !first.properties) return null;

  for (const key of Object.keys(first.properties)) {
    const values = new Set();
    let everyVariantHasLiteral = true;
    for (const variant of variants) {
      if (!variant || variant.type !== "object" || !variant.properties) {
        everyVariantHasLiteral = false;
        break;
      }
      const value = literalPropertyValue(variant.properties[key]);
      if (value === NO_LITERAL || values.has(value)) {
        everyVariantHasLiteral = false;
        break;
      }
      values.add(value);
    }
    if (everyVariantHasLiteral) return key;
  }

  return null;
}

function literalPropertyValue(schema) {
  if (!schema || typeof schema !== "object") return NO_LITERAL;
  if (Object.hasOwn(schema, "const")) return schema.const;
  if (Array.isArray(schema.enum) && schema.enum.length === 1) return schema.enum[0];
  return NO_LITERAL;
}
