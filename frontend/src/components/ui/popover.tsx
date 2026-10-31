"use client";

import * as React from "react";
import { cn } from "cn";
import { Popover as PopoverPrimitive } from "radix-ui";

interface PopoverProps extends React.ComponentProps<
  typeof PopoverPrimitive.Root
> {}

function Popover({ ...props }: PopoverProps) {
  return <PopoverPrimitive.Root data-slot="popover" {...props} />;
}

interface PopoverTriggerProps extends React.ComponentProps<
  typeof PopoverPrimitive.Trigger
> {}

function PopoverTrigger({ ...props }: PopoverTriggerProps) {
  return <PopoverPrimitive.Trigger data-slot="popover-trigger" {...props} />;
}

interface PopoverContentProps extends React.ComponentProps<
  typeof PopoverPrimitive.Content
> {}

function PopoverContent({
  className,
  align = "center",
  sideOffset = 4,
  ...props
}: PopoverContentProps) {
  return (
    <PopoverPrimitive.Portal>
      <PopoverPrimitive.Content
        data-slot="popover-content"
        align={align}
        sideOffset={sideOffset}
        className={cn(
          "z-50 w-72 origin-(--radix-popover-content-transform-origin) rounded-md border bg-popover p-4 text-popover-foreground shadow-md outline-hidden data-[side=bottom]:slide-in-from-top-2 data-[side=left]:slide-in-from-right-2 data-[side=right]:slide-in-from-left-2 data-[side=top]:slide-in-from-bottom-2 data-[state=open]:animate-in data-[state=open]:fade-in-0 data-[state=open]:zoom-in-95",
          className
        )}
        {...props}
      />
    </PopoverPrimitive.Portal>
  );
}

interface PopoverAnchorProps extends React.ComponentProps<
  typeof PopoverPrimitive.Anchor
> {}

function PopoverAnchor({ ...props }: PopoverAnchorProps) {
  return <PopoverPrimitive.Anchor data-slot="popover-anchor" {...props} />;
}

interface PopoverHeaderProps extends React.ComponentProps<"div"> {}

function PopoverHeader({ className, ...props }: PopoverHeaderProps) {
  return (
    <div
      data-slot="popover-header"
      className={cn("flex flex-col gap-1 text-sm", className)}
      {...props}
    />
  );
}

interface PopoverTitleProps extends React.ComponentProps<"h2"> {}

function PopoverTitle({ className, ...props }: PopoverTitleProps) {
  return (
    <div
      data-slot="popover-title"
      className={cn("font-medium", className)}
      {...props}
    />
  );
}

interface PopoverDescriptionProps extends React.ComponentProps<"p"> {}

function PopoverDescription({ className, ...props }: PopoverDescriptionProps) {
  return (
    <p
      data-slot="popover-description"
      className={cn("text-muted-foreground", className)}
      {...props}
    />
  );
}

export {
  Popover,
  PopoverAnchor,
  PopoverContent,
  PopoverDescription,
  PopoverHeader,
  PopoverTitle,
  PopoverTrigger,
};
