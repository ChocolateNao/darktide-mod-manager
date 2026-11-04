import type { ReactNode } from "react";

/**
 * The title row every view shares: one h1 on the left, that view's actions on
 * the right. A fixed height keeps the content below on the same line across
 * views, whether or not a view has actions.
 */
interface PageHeaderProps {
  title: string;
  children?: ReactNode;
}

export function PageHeader({ title, children }: PageHeaderProps) {
  return (
    <header className="flex h-11 shrink-0 items-center gap-2 border-b px-4">
      <h1 className="text-sm font-semibold">{title}</h1>
      {children ? (
        <div className="ml-auto flex min-w-0 items-center gap-2">
          {children}
        </div>
      ) : null}
    </header>
  );
}
