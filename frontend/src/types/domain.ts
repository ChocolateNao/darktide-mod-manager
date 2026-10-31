/**
 * camelCase mirrors of the Go domain types.
 *
 * The bridge parses the wire (Go JSON, snake_case, single-letter tags) into
 * these, so no component ever sees `mod_a` or `Entries`.
 */

export type ModInstallMode = "mods_folder" | "game_root" | (string & {});

export interface Mod {
  id: string;
  name: string;
  version: string;
  author: string;
  description: string;
  sourceId: string;
  /** Arbitrary per-source metadata. Only the sources that define keys are read. */
  sourceData: Record<string, unknown>;
  gameId?: string;
  gameVersion?: string;
  modFolderName?: string;
  installMode?: ModInstallMode;
  hash?: string;
  localPath: string;
  sourceUrl?: string;
  downloadUrl?: string;
  updateUrl?: string;
  dependencies: string[];
  conflictsWith: string[];
  tags: string[];
  issues: string[];
  priority: number;
  enabled: boolean;
  /** Found in the game folder but not installed by the manager. */
  external: boolean;
  installedAt: Date | null;
  updatedAt: Date | null;
  hooks: ModHooks;
}

export type HookPhase =
  | "pre_install"
  | "post_install"
  | "pre_enable"
  | "post_enable"
  | "pre_disable"
  | "post_disable"
  | "pre_uninstall"
  | "post_uninstall"
  | "pre_update"
  | "post_update";

export type ScriptLanguage = "lua" | "javascript";

export interface HookScript {
  language: ScriptLanguage;
  content: string;
  workingDir?: string;
  /** `omitempty` on the wire, so an unset environment is an absent array. */
  env?: string[];
  /** Seconds. `HookTimeout` returns milliseconds; both are normalised there. */
  timeout?: number;
  /** The user's label for this script; blank falls back to a generated one. */
  name?: string;
  /** Paused without being deleted — the executor skips it, silently. */
  disabled?: boolean;
}

/** Every phase is present as a key so the editor never has to check. */
export type ModHooks = Record<HookPhase, HookScript[]>;

export type ConflictType =
  | "file_overlap"
  | "duplicate_id"
  | "load_order"
  | "version_mismatch"
  | "declared"
  | (string & {});

export type ConflictSeverity = "warning" | "error" | (string & {});

export interface ConflictReport {
  /** `ResolveConflict` takes this; the backend stamps it during detection. */
  id: string;
  type: ConflictType;
  modA?: Mod;
  modB?: Mod;
  details: string;
  severity: ConflictSeverity;
  recommendation: string;
}

export type ConflictResolution =
  | "keep-both"
  | "disable-mod-a"
  | "disable-mod-b"
  | "uninstall-mod-a"
  | "uninstall-mod-b";

export interface ModUpdateInfo {
  modId: string;
  localVersion: string;
  remoteVersion: string;
  sourceId: string;
  available: boolean;
  releaseNotes?: string;
  downloadUrl?: string;
  changelogUrl?: string;
}

export type LinkState = "missing" | "foreign" | "ok" | "broken" | "retargeted";

/** One entry inside a mod's installed folder, relative to that folder. */
export interface ModFile {
  path: string;
  name: string;
  size: number;
  isDir: boolean;
}

export interface ModLinkStatus {
  /** Relative path → link state. */
  entries: Record<string, LinkState>;
  modId: string;
  damaged: number;
}

export type SourceLinkFallback = "download" | "browse" | "update" | "";

export interface ModSourceLink {
  modId: string;
  sourceId?: string;
  sourceUrl?: string;
  downloadUrl?: string;
  updateUrl?: string;
  fallback: SourceLinkFallback;
}

/** The link target names are a Go enum; the UI asks for them. */
export type ModLinkTarget =
  "auto" | "source" | "download" | "update" | (string & {});

export interface ModSourceInfo {
  id: string;
  name: string;
  description: string;
  version: string;
  supportsSearch: boolean;
  supportsAuth: boolean;
  requiresApiKey: boolean;
  enabled: boolean;
  authenticated: boolean;
  /** The key itself is never returned, so a form never pre-fills one. */
  hasApiKey: boolean;
}

export interface SearchQuery {
  query: string;
  gameVersion?: string;
  sortBy?: "relevance" | "updated" | "downloads" | "name" | (string & {});
  tags?: string[];
  page?: number;
  pageSize?: number;
}

export interface GameInfo {
  id: string;
  displayName: string;
  description?: string;
  defaultInstallMethod: string;
  installModes: ModInstallMode[];
  isDefault: boolean;
  isActive: boolean;
}

export interface GameInstallation {
  gameId?: string;
  path: string;
  source: string;
  appId: string;
  version: string;
  modsPath: string;
  platform: string;
  notes?: string;
  valid: boolean;
  detectedAt: Date | null;
}

export interface GameSettings {
  /** A built-in icon choice or a data URL image; blank uses the built-in default. */
  icon?: string;
  path?: string;
  source?: string;
  modsFolderOverride?: string;
  installMethod?: string;
  autoDetect: boolean;
}

export interface UIConfig {
  theme: "light" | "dark" | "system";
  language: string;
  compactMode: boolean;
}

export interface AdvancedConfig {
  enableExperimental: boolean;
}

export interface StorageConfig {
  path: string;
  backupRetentionDays: number;
}

export interface CacheConfig {
  path: string;
  maxSizeGb: number;
  maxAgeDays: number;
  verifyHashes: boolean;
}

export interface LoggingConfig {
  level: string;
  directory?: string;
  redact?: string[];
  maxSizeMb: number;
  maxBackups: number;
  maxAgeDays: number;
  compress: boolean;
}

export interface AutoUpdateConfig {
  /** Absent means "inherit": `ApplyDefaults` leaves an absent key alone, so JSON cannot tell "unset" from "false". */
  notifyOnUpdate?: boolean;
  lastCheck?: string;
  enabled: boolean;
  checkIntervalHours: number;
  downloadAutomatically: boolean;
}

export type ScriptPermissionKey =
  | "fsRead"
  | "fsWrite"
  | "configRead"
  | "configWrite"
  | "storageRead"
  | "envRead";

export interface ScriptPermissions {
  fsRead: boolean;
  fsWrite: boolean;
  configRead: boolean;
  configWrite: boolean;
  storageRead: boolean;
  envRead: boolean;
  timeoutSeconds?: number;
}

export interface HooksPermissionsConfig {
  default: ScriptPermissions;
  games: Record<string, ScriptPermissions>;
  mods: Record<string, ScriptPermissions>;
}

export interface HooksConfig {
  permissions: HooksPermissionsConfig;
  defaultTimeout: string;
  maxTimeout: string;
}

export interface SecurityConfig {
  allowActionCommands: boolean;
}

export interface SourceConfig {
  options: Record<string, string>;
  apiKey?: string;
  paths: string[];
  enabled: boolean;
}

export interface P2PSharingConfig {
  stunServers: string[];
  turnRelays: string[];
  expiryDays: number;
  chunkSizeKb?: number;
}

export interface SharingConfig {
  p2p: P2PSharingConfig;
}

export interface CloudSharingConfig {
  providers: Record<string, Record<string, string>>;
  defaultProvider: string;
  fallbackProviders: string[];
  shareExpiryDays: number;
  autoInstall: boolean;
}

export interface AppConfig {
  hooks: HooksConfig;
  sources: Record<string, SourceConfig>;
  games: Record<string, GameSettings>;
  activeGame: string;
  ui: UIConfig;
  advanced: AdvancedConfig;
  storage: StorageConfig;
  autoUpdate: AutoUpdateConfig;
  cache: CacheConfig;
  cloudSharing: CloudSharingConfig;
  sharing: SharingConfig;
  logging: LoggingConfig;
  security: SecurityConfig;
}

export interface CacheInfo {
  path: string;
  fileCount: number;
  totalSize: number;
  oldest: Date | null;
  newest: Date | null;
}

export type LogLevel = "trace" | "debug" | "info" | "warn" | "error";

export interface LogSettings {
  level: string;
  directory: string;
  file: string;
  maxSizeMb: number;
  maxBackups: number;
  maxAgeDays: number;
  compress: boolean;
  consoleOnly: boolean;
  hasRedactFields: boolean;
}

export interface LogLine {
  timestamp: string;
  level: string;
  caller?: string;
  message: string;
  modId?: string;
  /** Already redacted by the sink */
  fields?: Record<string, unknown>;
}

export interface LogFile {
  name: string;
  path: string;
  size: number;
  modTime: Date | null;
  rotated: boolean;
  compressed: boolean;
}

export interface PackedMod {
  id: string;
  hash: string;
  format: string;
  size: number;
}

export interface SharedMod {
  archive?: PackedMod;
  name: string;
  version: string;
  author: string;
  description: string;
  fileIds: string[];
  dependencies: string[];
}

export interface P2PSharedFile {
  fileId: string;
  name: string;
  modId: string;
  hashSha256: string;
  size: number;
}

export interface P2PShareLink {
  createdAt: Date | null;
  link: string;
  sender?: string;
  offer: {
    offerId: string;
    gameId: string;
    senderName?: string;
    mods: SharedMod[];
    files: P2PSharedFile[];
    sourceLinks: ModSourceLink[];
    stunServers: string[];
    createdAt: Date | null;
    expiresAt: Date | null;
  };
}

export interface P2PAnswerLink {
  createdAt: Date | null;
  link: string;
  offerId: string;
  receiver?: string;
}

export interface P2PSharePreview {
  createdAt: Date | null;
  expiresAt: Date | null;
  offerId: string;
  gameId: string;
  sender?: string;
  mods: SharedMod[];
  files: P2PSharedFile[];
  sourceLinks: ModSourceLink[];
  totalSize: number;
  expired: boolean;
}

export type P2PShareState =
  | "created"
  | "offered"
  | "connecting"
  | "transferring"
  | "verifying"
  | "completed"
  | "rejected"
  | "expired"
  | "failed";

export interface P2PShareSession {
  updatedAt: Date | null;
  createdAt: Date | null;
  preview?: P2PSharePreview;
  expiresAt: Date | null;
  state: P2PShareState;
  failReason?: string;
  link?: string;
  offerId: string;
  role: "sender" | "recipient" | (string & {});
  gameId: string;
  modIds: string[];
  totalSize: number;
  transferredBytes: number;
}

export type ActionOptionType = "string" | "path" | "bool" | "number" | "select";

export interface ActionOption {
  name: string;
  type: ActionOptionType;
  label: string;
  labelKey?: string;
  description: string;
  descriptionKey?: string;
  placeholder: string;
  placeholderKey?: string;
  choices: string[];
  required: boolean;
  default?: unknown;
}

export interface GameAction {
  id: string;
  gameId: string;
  displayName: string;
  displayNameKey?: string;
  description: string;
  descriptionKey?: string;
  category: string;
  categoryKey?: string;
  icon: string;
  platform: string;
  options: ActionOption[];
  requiresGame: boolean;
  dangerous: boolean;
}

export interface ActionDetail {
  labelKey?: string;
  label: string;
  value: string;
}

export interface GameActionResult {
  success: boolean;
  message: string;
  messageKey?: string;
  messageArgs?: Record<string, string | number>;
  details?: ActionDetail[];
  output: string;
  /** Deliberately `unknown`: the renderer pretty-prints whatever Go returns. */
  data: unknown;
}

export interface LifecycleVocabulary {
  eventTypes: string[];
  hookTypes: string[];
  hookPhases: string[];
}

/**
 * A projection of what a dropped path resolved to: the overlay only has to name
 * the mod it is about to install.
 */
export interface DroppedMod {
  sourceId: string;
  modId: string;
  name: string;
  version: string;
  author?: string;
  path: string;
}
