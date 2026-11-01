import { useEffect, useMemo } from "react";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";

import { pickDirectory } from "@/bridge/dialogs";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { t, tOr } from "@/lib/i18n";
import type { ActionFormValues } from "@/lib/schemas/action";
import { buildActionOptionSchema } from "@/lib/schemas/action";
import type { ActionOption } from "@/types/domain";

interface PathFieldProps {
  option: ActionOption;
  value: string;
  onChange: (next: string) => void;
}

function PathField({ option, value, onChange }: PathFieldProps) {
  return (
    <div className="flex gap-2">
      <Input
        id={option.name}
        value={value}
        placeholder={tOr(option.placeholderKey, option.placeholder)}
        onChange={(event) => onChange(event.target.value)}
        data-selectable
      />
      <Button
        type="button"
        variant="secondary"
        onClick={async () => {
          const choice = await pickDirectory(value);

          if (choice.kind === "chosen") {
            onChange(choice.path);
          }
        }}
      >
        {t("path.browse")}
      </Button>
    </div>
  );
}

function defaultsFor(options: readonly ActionOption[]): ActionFormValues {
  const values: ActionFormValues = {};

  for (const option of options) {
    if (option.default !== undefined && option.default !== null) {
      values[option.name] = option.default;
    } else if (option.type === "bool") {
      values[option.name] = false;
    } else if (option.type === "select" && option.choices[0] !== undefined) {
      values[option.name] = option.choices[0];
    } else {
      values[option.name] = "";
    }
  }

  return values;
}

/**
 * Renders one game action from its declaration: control and validation both
 * derive from `ActionOption`, so a game plugin needs no frontend change.
 */
interface ActionFormProps {
  action: {
    id: string;
    displayName: string;
    displayNameKey?: string;
    options: ActionOption[];
    dangerous: boolean;
  };
  presetArgs?: Record<string, unknown>;
  onSubmit: (args: Record<string, unknown>) => void;
  busy?: boolean;
}

export function ActionForm({
  action,
  presetArgs,
  onSubmit,
  busy,
}: ActionFormProps) {
  const schema = useMemo(
    () => buildActionOptionSchema(action.options),
    [action.options]
  );

  const form = useForm<ActionFormValues>({
    resolver: zodResolver(schema),
    defaultValues: { ...defaultsFor(action.options), ...presetArgs },
  });

  // The dialog stays mounted while the request changes (a mod row opens a
  // different action into the same host), so the form has to be reset by hand.
  useEffect(() => {
    form.reset({ ...defaultsFor(action.options), ...presetArgs });
  }, [action.id, action.options, presetArgs, form]);

  if (action.options.length === 0) {
    return (
      <form
        onSubmit={form.handleSubmit(() => onSubmit({}))}
        className="flex flex-col gap-4"
      >
        <Button
          type="submit"
          variant={action.dangerous ? "destructive" : "default"}
          loading={busy}
        >
          {t("actions.run", {
            name: tOr(action.displayNameKey, action.displayName),
          })}
        </Button>
      </form>
    );
  }

  return (
    <form
      onSubmit={form.handleSubmit((values) => onSubmit(values))}
      className="flex flex-col gap-4"
    >
      {action.options.map((option) => (
        <div key={option.name} className="flex flex-col gap-1.5">
          <Label htmlFor={option.name}>
            {tOr(option.labelKey, option.label || option.name)}
            {option.required ? (
              <span className="text-destructive"> *</span>
            ) : null}
          </Label>
          <OptionControl
            option={option}
            value={form.watch(option.name)}
            onChange={(next) =>
              form.setValue(option.name, next, { shouldDirty: true })
            }
            onBlur={() => void form.trigger(option.name)}
          />
          {option.description ? (
            <p className="text-xs text-muted-foreground">
              {tOr(option.descriptionKey, option.description)}
            </p>
          ) : null}
          {/* Always mounted so validation swaps text instead of moving the fields below. */}
          <p
            data-slot="field-error"
            aria-live="polite"
            className="min-h-4 text-xs text-destructive"
          >
            {form.formState.errors[option.name]
              ? String(
                  form.formState.errors[option.name]?.message ??
                    t("actions.invalidValue")
                )
              : null}
          </p>
        </div>
      ))}

      {/* The label stays put while running: swapping it for "Running…" changes
          the button's width twice, so a spinner sits beside it instead. */}
      <Button
        type="submit"
        variant={action.dangerous ? "destructive" : "default"}
        loading={busy}
      >
        {t("actions.run", {
          name: tOr(action.displayNameKey, action.displayName),
        })}
      </Button>
    </form>
  );
}

/* `onBlur` is typed loosely because only RHF knows what it wants: the same
 * callback satisfies a DOM focus event on an `Input` and nothing on a `Switch`. */
interface OptionControlProps {
  option: ActionOption;
  value: unknown;
  onChange: (next: unknown) => void;
  onBlur: (event?: unknown) => void;
}

function OptionControl({
  option,
  value,
  onChange,
  onBlur,
}: OptionControlProps) {
  const placeholder = tOr(option.placeholderKey, option.placeholder);

  switch (option.type) {
    case "bool":
      return (
        <div className="flex h-9 items-center">
          <Switch
            id={option.name}
            checked={Boolean(value)}
            onCheckedChange={onChange}
            onBlur={onBlur}
          />
        </div>
      );
    case "select":
      return option.choices.length ? (
        <Select value={String(value ?? "")} onValueChange={onChange}>
          <SelectTrigger id={option.name} className="w-full">
            <SelectValue placeholder={placeholder} />
          </SelectTrigger>
          <SelectContent>
            {option.choices.map((choice) => (
              <SelectItem key={choice} value={choice}>
                {choice}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      ) : (
        <Input
          id={option.name}
          value={String(value ?? "")}
          placeholder={placeholder}
          onChange={(event) => onChange(event.target.value)}
          onBlur={onBlur}
        />
      );
    case "path":
      return (
        <PathField
          option={option}
          value={String(value ?? "")}
          onChange={(next) => onChange(next)}
        />
      );
    case "number":
      return (
        <Input
          id={option.name}
          inputMode="numeric"
          value={String(value ?? "")}
          placeholder={placeholder}
          onChange={(event) => onChange(event.target.value)}
          onBlur={onBlur}
        />
      );
    default:
      return (
        <Input
          id={option.name}
          value={String(value ?? "")}
          placeholder={placeholder}
          onChange={(event) => onChange(event.target.value)}
          onBlur={onBlur}
        />
      );
  }
}
