import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { QueryClientProvider } from "@tanstack/react-query";

import { TooltipProvider } from "./components/ui/tooltip";
import { queryClient } from "./lib/queryClient";
import { App } from "./App";

import "./style.css";

const container = document.getElementById("root");

if (!container) {
  throw new Error("index.html is missing #root");
}

createRoot(container).render(
  <StrictMode>
    {/* Radix throws when a Tooltip renders outside its provider, and the rail's
        tooltips mount first, so the provider sits at the root. */}
    <TooltipProvider>
      <QueryClientProvider client={queryClient}>
        <App />
      </QueryClientProvider>
    </TooltipProvider>
  </StrictMode>
);
