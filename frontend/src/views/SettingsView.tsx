import { useState } from "react";

import { AdvancedTab } from "@/components/settings/AdvancedTab";
import { AppTab } from "@/components/settings/AppTab";
import { CacheTab } from "@/components/settings/CacheTab";
import { GamesTab } from "@/components/settings/GamesTab";
import { HooksTab } from "@/components/settings/HooksTab";
import { ModUpdatesTab } from "@/components/settings/ModUpdatesTab";
import { SecurityTab } from "@/components/settings/SecurityTab";
import { SharingTab } from "@/components/settings/SharingTab";
import { SourcesTab } from "@/components/settings/SourcesTab";
import { StorageTab } from "@/components/settings/StorageTab";
import { ScrollArea } from "@/components/ui/scroll-area";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { t } from "@/lib/i18n";

/**
 * Ten tabs, an explicit list: each tab is a *decision about what a user changes
 * together*, so "Sharing" and "Mod updates" stand on their own — a settings key
 * without a home is a setting nobody finds.
 */
const TABS = [
  { id: "app", label: "settings.tab.app", render: () => <AppTab /> },
  { id: "games", label: "settings.tab.games", render: () => <GamesTab /> },
  {
    id: "storage",
    label: "settings.tab.storage",
    render: () => <StorageTab />,
  },
  { id: "cache", label: "settings.tab.cache", render: () => <CacheTab /> },
  {
    id: "sharing",
    label: "settings.tab.sharing",
    render: () => <SharingTab />,
  },
  {
    id: "sources",
    label: "settings.tab.sources",
    render: () => <SourcesTab />,
  },
  { id: "hooks", label: "settings.tab.hooks", render: () => <HooksTab /> },
  {
    id: "security",
    label: "settings.tab.security",
    render: () => <SecurityTab />,
  },
  {
    id: "modUpdates",
    label: "settings.tab.updates",
    render: () => <ModUpdatesTab />,
  },
  {
    id: "advanced",
    label: "settings.tab.advanced",
    render: () => <AdvancedTab />,
  },
] as const;

export default function SettingsView() {
  const [tab, setTab] = useState<string>("app");

  return (
    <div className="flex h-full min-h-0 flex-col">
      <Tabs
        value={tab}
        onValueChange={setTab}
        className="flex min-h-0 flex-1 flex-col"
      >
        <ScrollArea className="w-full shrink-0 border-b">
          <TabsList className="h-10 w-max justify-start rounded-none border-b-0 bg-transparent px-4">
            {TABS.map((entry) => (
              <TabsTrigger
                key={entry.id}
                value={entry.id}
                className="data-[state=active]:border-b-2"
              >
                {t(entry.label)}
              </TabsTrigger>
            ))}
          </TabsList>
        </ScrollArea>

        {TABS.map((entry) => (
          <TabsContent
            key={entry.id}
            value={entry.id}
            className="min-h-0 flex-1 overflow-hidden"
          >
            <ScrollArea className="h-full">
              <div className="mx-auto max-w-3xl p-6">{entry.render()}</div>
            </ScrollArea>
          </TabsContent>
        ))}
      </Tabs>
    </div>
  );
}
