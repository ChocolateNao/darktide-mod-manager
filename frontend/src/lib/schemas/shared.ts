import { z } from "zod";

/**
 * Wire primitives.
 *
 * Go's zero `time.Time` is indistinguishable from "absent", so timestamps map
 * to `null`; `.optional()` keeps an `omitempty` field a genuinely optional key.
 */
export const wireDate = z
  .string()
  .nullable()
  .transform((value) => {
    if (!value) return null;
    const parsed = new Date(value);
    return Number.isNaN(parsed.getTime()) ? null : parsed;
  })
  .optional();

/** `omitempty` turns an empty string into an absent key; both mean "unset". */
export const optionalText = z
  .string()
  .nullish()
  .transform((value) => (value ? value : undefined))
  .optional();

export const textList = z
  .array(z.string())
  .nullish()
  .transform((value) => value ?? [])
  .optional();

export const int = z.number().int();
