import * as React from "react";
import { cn } from "cn";
import { Switch as SwitchPrimitive } from "radix-ui";

/* Sizes are chunky so a switch does not disappear between two rows of copy in the dense settings rows. */
const SIZES = {
  sm: {
    root: "h-4 w-7",
    thumb: "size-3.5 data-[state=checked]:translate-x-[calc(100%-2px)]",
  },
  default: {
    root: "h-5 w-9",
    thumb: "size-4 data-[state=checked]:translate-x-[calc(100%-2px)]",
  },
  lg: {
    root: "h-6 w-11",
    thumb: "size-5 data-[state=checked]:translate-x-[calc(100%-4px)]",
  },
} as const;

interface SwitchProps extends React.ComponentProps<
  typeof SwitchPrimitive.Root
> {
  size?: keyof typeof SIZES;
}

function Switch({ className, size = "default", ...props }: SwitchProps) {
  return (
    <SwitchPrimitive.Root
      data-slot="switch"
      data-size={size}
      className={cn(
        "peer group/switch inline-flex shrink-0 cursor-pointer items-center rounded-full border-2 border-transparent shadow-xs transition-colors outline-none focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/50 disabled:cursor-not-allowed disabled:opacity-50",
        SIZES[size].root,
        "data-[state=checked]:bg-primary data-[state=unchecked]:bg-input dark:data-[state=unchecked]:bg-input/80",
        className
      )}
      {...props}
    >
      <SwitchPrimitive.Thumb
        data-slot="switch-thumb"
        className={cn(
          "pointer-events-none block rounded-full bg-background shadow-sm ring-0 transition-transform data-[state=unchecked]:translate-x-0",
          SIZES[size].thumb,
          "dark:data-[state=checked]:bg-primary-foreground dark:data-[state=unchecked]:bg-foreground"
        )}
      />
    </SwitchPrimitive.Root>
  );
}

export { Switch };
