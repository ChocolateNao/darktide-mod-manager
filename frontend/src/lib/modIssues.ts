import { t } from "@/lib/i18n";

/** Human label for a mod issue key. Unknown keys stay as the backend wrote them. */
export function issueLabel(issue: string): string {
  return issue === "multiple_manifests"
    ? t("mods.issue.multipleManifests")
    : issue;
}

export function issueSummary(issues: string[]): string {
  return issues.map(issueLabel).join(" · ");
}
