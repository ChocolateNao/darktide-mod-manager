/**
 * A mirror of `pkg/errors`' Go constants: views switch on the code, so a missing
 * code is one a view cannot branch on.
 */
export const BRIDGE_ERROR_CODES = [
  "UNKNOWN",
  "CONFIG_INVALID",
  "CONFIG_IO",
  "NOT_FOUND",
  "INVALID_ARGUMENT",
  "MOD_NOT_FOUND",
  "MOD_ALREADY_EXISTS",
  "MOD_NOT_INSTALLED",
  "INVALID_MOD",
  "DEPENDENCY_MISSING",
  "DEPENDENCY_CYCLE",
  "HOOK_FAILED",
  "HOOK_TIMEOUT",
  "HOOK_INVALID",
  "INSTALL_FAILED",
  "UNINSTALL_FAILED",
  "TRASH_UNAVAILABLE",
  "OPERATION_CANCELED",
  "NO_STORAGE_PATH",
  "GAME_NOT_FOUND",
  "GAME_INVALID",
  "MODS_PATH_NOT_DIR",
  "SOURCE_UNAVAILABLE",
  "SOURCE_NOT_FOUND",
  "SOURCE_EXISTS",
  "SOURCE_DISABLED",
  "SOURCE_REJECTED",
  "AUTH_FAILED",
  "AUTH_REQUIRED",
  "DOWNLOAD_FAILED",
  "SEARCH_FAILED",
  "INTEGRITY_CHECK_FAILED",
  "CACHE_MISS",
  "CACHE_CORRUPT",
  "RUNNER_NOT_FOUND",
  "HOOK_DENIED",
  "DUPLICATE",
  "ACTION_NOT_FOUND",
  "ACTION_UNAVAILABLE",
  "ACTION_FAILED",
  "CLOUD_PROVIDER_NOT_FOUND",
  "CLOUD_AUTH_REQUIRED",
  "SHARE_EXPIRED",
  "SHARE_INVALID",
  "SHARE_REVOKED",
  "GAME_NOT_REGISTERED",
  "FILE_TOO_LARGE",
  "INTEGRITY_MISMATCH",
  "ALL_PROVIDERS_FAILED",
  "SHARE_NOT_FOUND",
  "CLOUD_UPLOAD_FAILED",
  "CLOUD_DOWNLOAD_FAILED",
] as const;

export type BridgeErrorCode = (typeof BRIDGE_ERROR_CODES)[number];

/**
 * A plain list, since retryable codes are rare: a `NOT_FOUND` from a background
 * floor poll would otherwise retry twice and log a failure the user never caused.
 */
export const TRANSIENT_ERROR_CODES = new Set<BridgeErrorCode>([
  "CACHE_CORRUPT",
  "DOWNLOAD_FAILED",
  "SEARCH_FAILED",
  "SOURCE_UNAVAILABLE",
]);

export const isBridgeErrorCode = (
  value: unknown,
  code: BridgeErrorCode
): boolean => value === code;

/** A rejected bridge call, normalized so views never inspect message text. */
export class BridgeError extends Error {
  readonly code: BridgeErrorCode;
  readonly cause?: unknown;

  constructor(code: BridgeErrorCode, message: string, cause?: unknown) {
    super(message);
    this.name = "BridgeError";
    this.code = code;
    this.cause = cause;
  }

  get cancelled(): boolean {
    return this.code === "OPERATION_CANCELED";
  }

  get retryable(): boolean {
    return TRANSIENT_ERROR_CODES.has(this.code);
  }
}
