export namespace api {
	
	export class DefaultPaths {
	    storage: string;
	    cache: string;
	
	    static createFrom(source: any = {}) {
	        return new DefaultPaths(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.storage = source["storage"];
	        this.cache = source["cache"];
	    }
	}
	export class DeleteDataOptions {
	    ModData: boolean;
	    Config: boolean;
	
	    static createFrom(source: any = {}) {
	        return new DeleteDataOptions(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.ModData = source["ModData"];
	        this.Config = source["Config"];
	    }
	}
	export class DroppedMod {
	    source_id: string;
	    mod_id: string;
	    name: string;
	    version: string;
	    author?: string;
	    path: string;
	
	    static createFrom(source: any = {}) {
	        return new DroppedMod(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.source_id = source["source_id"];
	        this.mod_id = source["mod_id"];
	        this.name = source["name"];
	        this.version = source["version"];
	        this.author = source["author"];
	        this.path = source["path"];
	    }
	}
	export class DroppedPathResult {
	    mod?: DroppedMod;
	    recognised: boolean;
	
	    static createFrom(source: any = {}) {
	        return new DroppedPathResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.mod = this.convertValues(source["mod"], DroppedMod);
	        this.recognised = source["recognised"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class GameInfo {
	    id: string;
	    display_name: string;
	    description?: string;
	    default_install_method: string;
	    install_modes: string[];
	    is_default: boolean;
	    is_active: boolean;
	
	    static createFrom(source: any = {}) {
	        return new GameInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.display_name = source["display_name"];
	        this.description = source["description"];
	        this.default_install_method = source["default_install_method"];
	        this.install_modes = source["install_modes"];
	        this.is_default = source["is_default"];
	        this.is_active = source["is_active"];
	    }
	}

}

export namespace commands {
	
	export class LogSettings {
	    level: string;
	    directory: string;
	    file: string;
	    max_size_mb: number;
	    max_backups: number;
	    max_age_days: number;
	    compress: boolean;
	    console_only: boolean;
	    has_redact_fields: boolean;
	
	    static createFrom(source: any = {}) {
	        return new LogSettings(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.level = source["level"];
	        this.directory = source["directory"];
	        this.file = source["file"];
	        this.max_size_mb = source["max_size_mb"];
	        this.max_backups = source["max_backups"];
	        this.max_age_days = source["max_age_days"];
	        this.compress = source["compress"];
	        this.console_only = source["console_only"];
	        this.has_redact_fields = source["has_redact_fields"];
	    }
	}
	export class ModFile {
	    path: string;
	    name: string;
	    size: number;
	    is_dir: boolean;
	
	    static createFrom(source: any = {}) {
	        return new ModFile(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.name = source["name"];
	        this.size = source["size"];
	        this.is_dir = source["is_dir"];
	    }
	}

}

export namespace domain {
	
	export class ActionCommand {
	    Path: string;
	    Platform: string;
	    Args: string[];
	
	    static createFrom(source: any = {}) {
	        return new ActionCommand(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.Path = source["Path"];
	        this.Platform = source["Platform"];
	        this.Args = source["Args"];
	    }
	}
	export class ActionDetail {
	    LabelKey: string;
	    Label: string;
	    Value: string;
	
	    static createFrom(source: any = {}) {
	        return new ActionDetail(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.LabelKey = source["LabelKey"];
	        this.Label = source["Label"];
	        this.Value = source["Value"];
	    }
	}
	export class ActionOption {
	    Default: any;
	    Name: string;
	    Type: string;
	    Label: string;
	    LabelKey: string;
	    Description: string;
	    DescriptionKey: string;
	    Placeholder: string;
	    PlaceholderKey: string;
	    Choices: string[];
	    Required: boolean;
	
	    static createFrom(source: any = {}) {
	        return new ActionOption(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.Default = source["Default"];
	        this.Name = source["Name"];
	        this.Type = source["Type"];
	        this.Label = source["Label"];
	        this.LabelKey = source["LabelKey"];
	        this.Description = source["Description"];
	        this.DescriptionKey = source["DescriptionKey"];
	        this.Placeholder = source["Placeholder"];
	        this.PlaceholderKey = source["PlaceholderKey"];
	        this.Choices = source["Choices"];
	        this.Required = source["Required"];
	    }
	}
	export class ActionScript {
	    Interpreter: string;
	    WorkDir: string;
	    Commands: ActionCommand[];
	    SuccessWhenEmpty: boolean;
	
	    static createFrom(source: any = {}) {
	        return new ActionScript(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.Interpreter = source["Interpreter"];
	        this.WorkDir = source["WorkDir"];
	        this.Commands = this.convertValues(source["Commands"], ActionCommand);
	        this.SuccessWhenEmpty = source["SuccessWhenEmpty"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class AdvancedConfig {
	    enable_experimental: boolean;
	
	    static createFrom(source: any = {}) {
	        return new AdvancedConfig(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.enable_experimental = source["enable_experimental"];
	    }
	}
	export class AutoUpdateConfig {
	    last_check?: string;
	    check_interval_hours: number;
	    enabled: boolean;
	    download_automatically: boolean;
	    notify_on_update: boolean;
	
	    static createFrom(source: any = {}) {
	        return new AutoUpdateConfig(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.last_check = source["last_check"];
	        this.check_interval_hours = source["check_interval_hours"];
	        this.enabled = source["enabled"];
	        this.download_automatically = source["download_automatically"];
	        this.notify_on_update = source["notify_on_update"];
	    }
	}
	export class CacheConfig {
	    path: string;
	    max_size_gb: number;
	    max_age_days: number;
	    verify_hashes: boolean;
	
	    static createFrom(source: any = {}) {
	        return new CacheConfig(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.max_size_gb = source["max_size_gb"];
	        this.max_age_days = source["max_age_days"];
	        this.verify_hashes = source["verify_hashes"];
	    }
	}
	export class CacheInfo {
	    // Go type: time
	    oldest: any;
	    // Go type: time
	    newest: any;
	    path: string;
	    file_count: number;
	    total_size: number;
	
	    static createFrom(source: any = {}) {
	        return new CacheInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.oldest = this.convertValues(source["oldest"], null);
	        this.newest = this.convertValues(source["newest"], null);
	        this.path = source["path"];
	        this.file_count = source["file_count"];
	        this.total_size = source["total_size"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class CloudSharingConfig {
	    providers?: Record<string, any>;
	    default_provider: string;
	    fallback_providers?: string[];
	    share_expiry_days: number;
	    auto_install: boolean;
	
	    static createFrom(source: any = {}) {
	        return new CloudSharingConfig(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.providers = source["providers"];
	        this.default_provider = source["default_provider"];
	        this.fallback_providers = source["fallback_providers"];
	        this.share_expiry_days = source["share_expiry_days"];
	        this.auto_install = source["auto_install"];
	    }
	}
	export class SecurityConfig {
	    allow_action_commands?: boolean;
	
	    static createFrom(source: any = {}) {
	        return new SecurityConfig(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.allow_action_commands = source["allow_action_commands"];
	    }
	}
	export class LoggingConfig {
	    level: string;
	    directory?: string;
	    redact?: string[];
	    max_size_mb: number;
	    max_backups: number;
	    max_age_days: number;
	    compress: boolean;
	
	    static createFrom(source: any = {}) {
	        return new LoggingConfig(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.level = source["level"];
	        this.directory = source["directory"];
	        this.redact = source["redact"];
	        this.max_size_mb = source["max_size_mb"];
	        this.max_backups = source["max_backups"];
	        this.max_age_days = source["max_age_days"];
	        this.compress = source["compress"];
	    }
	}
	export class P2PSharingConfig {
	    stun_servers?: string[];
	    turn_relays?: string[];
	    expiry_days: number;
	    chunk_size_kb?: number;
	
	    static createFrom(source: any = {}) {
	        return new P2PSharingConfig(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.stun_servers = source["stun_servers"];
	        this.turn_relays = source["turn_relays"];
	        this.expiry_days = source["expiry_days"];
	        this.chunk_size_kb = source["chunk_size_kb"];
	    }
	}
	export class SharingConfig {
	    p2p: P2PSharingConfig;
	
	    static createFrom(source: any = {}) {
	        return new SharingConfig(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.p2p = this.convertValues(source["p2p"], P2PSharingConfig);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class StorageConfig {
	    path: string;
	    backup_retention_days: number;
	
	    static createFrom(source: any = {}) {
	        return new StorageConfig(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.backup_retention_days = source["backup_retention_days"];
	    }
	}
	export class UIConfig {
	    theme: string;
	    language: string;
	    compact_mode: boolean;
	
	    static createFrom(source: any = {}) {
	        return new UIConfig(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.theme = source["theme"];
	        this.language = source["language"];
	        this.compact_mode = source["compact_mode"];
	    }
	}
	export class GameSettings {
	    icon?: string;
	    path?: string;
	    source?: string;
	    mods_folder_override?: string;
	    install_method?: string;
	    auto_detect: boolean;
	
	    static createFrom(source: any = {}) {
	        return new GameSettings(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.icon = source["icon"];
	        this.path = source["path"];
	        this.source = source["source"];
	        this.mods_folder_override = source["mods_folder_override"];
	        this.install_method = source["install_method"];
	        this.auto_detect = source["auto_detect"];
	    }
	}
	export class SourceConfig {
	    options?: Record<string, string>;
	    api_key?: string;
	    paths?: string[];
	    enabled: boolean;
	
	    static createFrom(source: any = {}) {
	        return new SourceConfig(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.options = source["options"];
	        this.api_key = source["api_key"];
	        this.paths = source["paths"];
	        this.enabled = source["enabled"];
	    }
	}
	export class ScriptPermissions {
	    fs_read: boolean;
	    fs_write: boolean;
	    config_read: boolean;
	    config_write: boolean;
	    storage_read: boolean;
	    env_read: boolean;
	    timeout_seconds?: number;
	
	    static createFrom(source: any = {}) {
	        return new ScriptPermissions(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.fs_read = source["fs_read"];
	        this.fs_write = source["fs_write"];
	        this.config_read = source["config_read"];
	        this.config_write = source["config_write"];
	        this.storage_read = source["storage_read"];
	        this.env_read = source["env_read"];
	        this.timeout_seconds = source["timeout_seconds"];
	    }
	}
	export class HooksPermissionsConfig {
	    games?: Record<string, ScriptPermissions>;
	    mods?: Record<string, ScriptPermissions>;
	    default: ScriptPermissions;
	
	    static createFrom(source: any = {}) {
	        return new HooksPermissionsConfig(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.games = this.convertValues(source["games"], ScriptPermissions, true);
	        this.mods = this.convertValues(source["mods"], ScriptPermissions, true);
	        this.default = this.convertValues(source["default"], ScriptPermissions);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class HooksConfig {
	    permissions: HooksPermissionsConfig;
	    default_timeout: number;
	    max_timeout: number;
	
	    static createFrom(source: any = {}) {
	        return new HooksConfig(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.permissions = this.convertValues(source["permissions"], HooksPermissionsConfig);
	        this.default_timeout = source["default_timeout"];
	        this.max_timeout = source["max_timeout"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class Config {
	    hooks: HooksConfig;
	    sources: Record<string, SourceConfig>;
	    games?: Record<string, GameSettings>;
	    active_game: string;
	    ui: UIConfig;
	    advanced: AdvancedConfig;
	    storage: StorageConfig;
	    auto_update: AutoUpdateConfig;
	    cache: CacheConfig;
	    cloud_sharing: CloudSharingConfig;
	    sharing: SharingConfig;
	    logging: LoggingConfig;
	    security: SecurityConfig;
	
	    static createFrom(source: any = {}) {
	        return new Config(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.hooks = this.convertValues(source["hooks"], HooksConfig);
	        this.sources = this.convertValues(source["sources"], SourceConfig, true);
	        this.games = this.convertValues(source["games"], GameSettings, true);
	        this.active_game = source["active_game"];
	        this.ui = this.convertValues(source["ui"], UIConfig);
	        this.advanced = this.convertValues(source["advanced"], AdvancedConfig);
	        this.storage = this.convertValues(source["storage"], StorageConfig);
	        this.auto_update = this.convertValues(source["auto_update"], AutoUpdateConfig);
	        this.cache = this.convertValues(source["cache"], CacheConfig);
	        this.cloud_sharing = this.convertValues(source["cloud_sharing"], CloudSharingConfig);
	        this.sharing = this.convertValues(source["sharing"], SharingConfig);
	        this.logging = this.convertValues(source["logging"], LoggingConfig);
	        this.security = this.convertValues(source["security"], SecurityConfig);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class HookScript {
	    language: string;
	    content: string;
	    working_dir?: string;
	    name?: string;
	    env?: string[];
	    timeout?: number;
	    disabled?: boolean;
	
	    static createFrom(source: any = {}) {
	        return new HookScript(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.language = source["language"];
	        this.content = source["content"];
	        this.working_dir = source["working_dir"];
	        this.name = source["name"];
	        this.env = source["env"];
	        this.timeout = source["timeout"];
	        this.disabled = source["disabled"];
	    }
	}
	export class ModHooks {
	    pre_install?: HookScript[];
	    post_install?: HookScript[];
	    pre_enable?: HookScript[];
	    post_enable?: HookScript[];
	    pre_disable?: HookScript[];
	    post_disable?: HookScript[];
	    pre_uninstall?: HookScript[];
	    post_uninstall?: HookScript[];
	    pre_update?: HookScript[];
	    post_update?: HookScript[];
	
	    static createFrom(source: any = {}) {
	        return new ModHooks(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.pre_install = this.convertValues(source["pre_install"], HookScript);
	        this.post_install = this.convertValues(source["post_install"], HookScript);
	        this.pre_enable = this.convertValues(source["pre_enable"], HookScript);
	        this.post_enable = this.convertValues(source["post_enable"], HookScript);
	        this.pre_disable = this.convertValues(source["pre_disable"], HookScript);
	        this.post_disable = this.convertValues(source["post_disable"], HookScript);
	        this.pre_uninstall = this.convertValues(source["pre_uninstall"], HookScript);
	        this.post_uninstall = this.convertValues(source["post_uninstall"], HookScript);
	        this.pre_update = this.convertValues(source["pre_update"], HookScript);
	        this.post_update = this.convertValues(source["post_update"], HookScript);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class Mod {
	    // Go type: time
	    updated_at: any;
	    // Go type: time
	    installed_at: any;
	    source_data?: Record<string, any>;
	    id: string;
	    name: string;
	    source_id: string;
	    game_id?: string;
	    author: string;
	    game_version?: string;
	    mod_folder_name?: string;
	    install_mode?: string;
	    description: string;
	    hash?: string;
	    version: string;
	    local_path: string;
	    update_url?: string;
	    download_url?: string;
	    source_url?: string;
	    hooks: ModHooks;
	    dependencies?: string[];
	    conflicts_with?: string[];
	    tags?: string[];
	    issues?: string[];
	    priority: number;
	    enabled: boolean;
	    external: boolean;
	
	    static createFrom(source: any = {}) {
	        return new Mod(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.updated_at = this.convertValues(source["updated_at"], null);
	        this.installed_at = this.convertValues(source["installed_at"], null);
	        this.source_data = source["source_data"];
	        this.id = source["id"];
	        this.name = source["name"];
	        this.source_id = source["source_id"];
	        this.game_id = source["game_id"];
	        this.author = source["author"];
	        this.game_version = source["game_version"];
	        this.mod_folder_name = source["mod_folder_name"];
	        this.install_mode = source["install_mode"];
	        this.description = source["description"];
	        this.hash = source["hash"];
	        this.version = source["version"];
	        this.local_path = source["local_path"];
	        this.update_url = source["update_url"];
	        this.download_url = source["download_url"];
	        this.source_url = source["source_url"];
	        this.hooks = this.convertValues(source["hooks"], ModHooks);
	        this.dependencies = source["dependencies"];
	        this.conflicts_with = source["conflicts_with"];
	        this.tags = source["tags"];
	        this.issues = source["issues"];
	        this.priority = source["priority"];
	        this.enabled = source["enabled"];
	        this.external = source["external"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ConflictReport {
	    id: string;
	    type: string;
	    mod_a?: Mod;
	    mod_b?: Mod;
	    details: string;
	    severity: string;
	    recommendation: string;
	
	    static createFrom(source: any = {}) {
	        return new ConflictReport(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.type = source["type"];
	        this.mod_a = this.convertValues(source["mod_a"], Mod);
	        this.mod_b = this.convertValues(source["mod_b"], Mod);
	        this.details = source["details"];
	        this.severity = source["severity"];
	        this.recommendation = source["recommendation"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class GameAction {
	    Script?: ActionScript;
	    ID: string;
	    GameID: string;
	    DisplayName: string;
	    DisplayNameKey: string;
	    Description: string;
	    DescriptionKey: string;
	    Category: string;
	    CategoryKey: string;
	    Icon: string;
	    Platform: string;
	    Options: ActionOption[];
	    RequiresGame: boolean;
	    Dangerous: boolean;
	
	    static createFrom(source: any = {}) {
	        return new GameAction(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.Script = this.convertValues(source["Script"], ActionScript);
	        this.ID = source["ID"];
	        this.GameID = source["GameID"];
	        this.DisplayName = source["DisplayName"];
	        this.DisplayNameKey = source["DisplayNameKey"];
	        this.Description = source["Description"];
	        this.DescriptionKey = source["DescriptionKey"];
	        this.Category = source["Category"];
	        this.CategoryKey = source["CategoryKey"];
	        this.Icon = source["Icon"];
	        this.Platform = source["Platform"];
	        this.Options = this.convertValues(source["Options"], ActionOption);
	        this.RequiresGame = source["RequiresGame"];
	        this.Dangerous = source["Dangerous"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class GameActionResult {
	    Data: any;
	    Message: string;
	    MessageKey: string;
	    MessageArgs: Record<string, any>;
	    Details: ActionDetail[];
	    Output: string;
	    Success: boolean;
	
	    static createFrom(source: any = {}) {
	        return new GameActionResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.Data = source["Data"];
	        this.Message = source["Message"];
	        this.MessageKey = source["MessageKey"];
	        this.MessageArgs = source["MessageArgs"];
	        this.Details = this.convertValues(source["Details"], ActionDetail);
	        this.Output = source["Output"];
	        this.Success = source["Success"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class GameInstallation {
	    // Go type: time
	    detected_at: any;
	    game_id?: string;
	    path: string;
	    source: string;
	    app_id: string;
	    version: string;
	    mods_path: string;
	    platform: string;
	    notes?: string;
	    valid: boolean;
	
	    static createFrom(source: any = {}) {
	        return new GameInstallation(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.detected_at = this.convertValues(source["detected_at"], null);
	        this.game_id = source["game_id"];
	        this.path = source["path"];
	        this.source = source["source"];
	        this.app_id = source["app_id"];
	        this.version = source["version"];
	        this.mods_path = source["mods_path"];
	        this.platform = source["platform"];
	        this.notes = source["notes"];
	        this.valid = source["valid"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	
	
	
	
	
	
	export class ModSourceInfo {
	    id: string;
	    name: string;
	    description: string;
	    version: string;
	    supports_search: boolean;
	    supports_auth: boolean;
	    requires_api_key: boolean;
	    enabled: boolean;
	    authenticated: boolean;
	    has_api_key: boolean;
	
	    static createFrom(source: any = {}) {
	        return new ModSourceInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.description = source["description"];
	        this.version = source["version"];
	        this.supports_search = source["supports_search"];
	        this.supports_auth = source["supports_auth"];
	        this.requires_api_key = source["requires_api_key"];
	        this.enabled = source["enabled"];
	        this.authenticated = source["authenticated"];
	        this.has_api_key = source["has_api_key"];
	    }
	}
	export class ModSourceLink {
	    m: string;
	    s?: string;
	    p?: string;
	    d?: string;
	    u?: string;
	    f?: string;
	
	    static createFrom(source: any = {}) {
	        return new ModSourceLink(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.m = source["m"];
	        this.s = source["s"];
	        this.p = source["p"];
	        this.d = source["d"];
	        this.u = source["u"];
	        this.f = source["f"];
	    }
	}
	export class ModUpdateInfo {
	    mod_id: string;
	    local_version: string;
	    remote_version: string;
	    source_id: string;
	    release_notes?: string;
	    download_url?: string;
	    changelog_url?: string;
	    available: boolean;
	
	    static createFrom(source: any = {}) {
	        return new ModUpdateInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.mod_id = source["mod_id"];
	        this.local_version = source["local_version"];
	        this.remote_version = source["remote_version"];
	        this.source_id = source["source_id"];
	        this.release_notes = source["release_notes"];
	        this.download_url = source["download_url"];
	        this.changelog_url = source["changelog_url"];
	        this.available = source["available"];
	    }
	}
	export class P2PAnswerLink {
	    // Go type: time
	    created_at: any;
	    link: string;
	    offer_id: string;
	    receiver?: string;
	
	    static createFrom(source: any = {}) {
	        return new P2PAnswerLink(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.created_at = this.convertValues(source["created_at"], null);
	        this.link = source["link"];
	        this.offer_id = source["offer_id"];
	        this.receiver = source["receiver"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class P2PSharedFile {
	    i: string;
	    n: string;
	    m: string;
	    h: string;
	    s: number;
	
	    static createFrom(source: any = {}) {
	        return new P2PSharedFile(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.i = source["i"];
	        this.n = source["n"];
	        this.m = source["m"];
	        this.h = source["h"];
	        this.s = source["s"];
	    }
	}
	export class PackedMod {
	    id: string;
	    h: string;
	    f: string;
	    s: number;
	
	    static createFrom(source: any = {}) {
	        return new PackedMod(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.h = source["h"];
	        this.f = source["f"];
	        this.s = source["s"];
	    }
	}
	export class SharedMod {
	    archive?: PackedMod;
	    name: string;
	    version: string;
	    author: string;
	    description: string;
	    files: string[];
	    deps: string[];
	
	    static createFrom(source: any = {}) {
	        return new SharedMod(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.archive = this.convertValues(source["archive"], PackedMod);
	        this.name = source["name"];
	        this.version = source["version"];
	        this.author = source["author"];
	        this.description = source["description"];
	        this.files = source["files"];
	        this.deps = source["deps"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class P2PShareOffer {
	    // Go type: time
	    c: any;
	    // Go type: time
	    e?: any;
	    o: string;
	    g: string;
	    n?: string;
	    m: SharedMod[];
	    f: P2PSharedFile[];
	    l?: ModSourceLink[];
	    s?: string[];
	
	    static createFrom(source: any = {}) {
	        return new P2PShareOffer(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.c = this.convertValues(source["c"], null);
	        this.e = this.convertValues(source["e"], null);
	        this.o = source["o"];
	        this.g = source["g"];
	        this.n = source["n"];
	        this.m = this.convertValues(source["m"], SharedMod);
	        this.f = this.convertValues(source["f"], P2PSharedFile);
	        this.l = this.convertValues(source["l"], ModSourceLink);
	        this.s = source["s"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class P2PShareLink {
	    // Go type: time
	    created_at: any;
	    link: string;
	    sender?: string;
	    offer: P2PShareOffer;
	
	    static createFrom(source: any = {}) {
	        return new P2PShareLink(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.created_at = this.convertValues(source["created_at"], null);
	        this.link = source["link"];
	        this.sender = source["sender"];
	        this.offer = this.convertValues(source["offer"], P2PShareOffer);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	export class P2PSharePreview {
	    // Go type: time
	    created_at: any;
	    // Go type: time
	    expires_at?: any;
	    offer_id: string;
	    game_id: string;
	    sender?: string;
	    mods: SharedMod[];
	    files: P2PSharedFile[];
	    source_links?: ModSourceLink[];
	    total_size: number;
	    expired: boolean;
	
	    static createFrom(source: any = {}) {
	        return new P2PSharePreview(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.created_at = this.convertValues(source["created_at"], null);
	        this.expires_at = this.convertValues(source["expires_at"], null);
	        this.offer_id = source["offer_id"];
	        this.game_id = source["game_id"];
	        this.sender = source["sender"];
	        this.mods = this.convertValues(source["mods"], SharedMod);
	        this.files = this.convertValues(source["files"], P2PSharedFile);
	        this.source_links = this.convertValues(source["source_links"], ModSourceLink);
	        this.total_size = source["total_size"];
	        this.expired = source["expired"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class P2PShareSession {
	    // Go type: time
	    updated_at: any;
	    // Go type: time
	    created_at: any;
	    preview?: P2PSharePreview;
	    // Go type: time
	    expires_at?: any;
	    state: string;
	    fail_reason?: string;
	    link?: string;
	    offer_id: string;
	    role: string;
	    game_id: string;
	    mod_ids: string[];
	    total_size: number;
	    transferred_bytes: number;
	
	    static createFrom(source: any = {}) {
	        return new P2PShareSession(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.updated_at = this.convertValues(source["updated_at"], null);
	        this.created_at = this.convertValues(source["created_at"], null);
	        this.preview = this.convertValues(source["preview"], P2PSharePreview);
	        this.expires_at = this.convertValues(source["expires_at"], null);
	        this.state = source["state"];
	        this.fail_reason = source["fail_reason"];
	        this.link = source["link"];
	        this.offer_id = source["offer_id"];
	        this.role = source["role"];
	        this.game_id = source["game_id"];
	        this.mod_ids = source["mod_ids"];
	        this.total_size = source["total_size"];
	        this.transferred_bytes = source["transferred_bytes"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	
	
	
	export class SearchQuery {
	    query: string;
	    game_version?: string;
	    sort_by?: string;
	    tags?: string[];
	    page?: number;
	    page_size?: number;
	
	    static createFrom(source: any = {}) {
	        return new SearchQuery(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.query = source["query"];
	        this.game_version = source["game_version"];
	        this.sort_by = source["sort_by"];
	        this.tags = source["tags"];
	        this.page = source["page"];
	        this.page_size = source["page_size"];
	    }
	}
	
	
	
	
	

}

export namespace logging {
	
	export class LogFile {
	    // Go type: time
	    mod_time: any;
	    name: string;
	    path: string;
	    size: number;
	    rotated: boolean;
	    compressed: boolean;
	
	    static createFrom(source: any = {}) {
	        return new LogFile(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.mod_time = this.convertValues(source["mod_time"], null);
	        this.name = source["name"];
	        this.path = source["path"];
	        this.size = source["size"];
	        this.rotated = source["rotated"];
	        this.compressed = source["compressed"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class LogLine {
	    timestamp: string;
	    level: string;
	    caller?: string;
	    message: string;
	    mod_id?: string;
	    fields?: Record<string, any>;
	
	    static createFrom(source: any = {}) {
	        return new LogLine(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.timestamp = source["timestamp"];
	        this.level = source["level"];
	        this.caller = source["caller"];
	        this.message = source["message"];
	        this.mod_id = source["mod_id"];
	        this.fields = source["fields"];
	    }
	}

}

export namespace services {
	
	export class InstallOptions {
	    Enable?: boolean;
	    SourceData: Record<string, any>;
	    Force: boolean;
	    PurgeCache: boolean;
	
	    static createFrom(source: any = {}) {
	        return new InstallOptions(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.Enable = source["Enable"];
	        this.SourceData = source["SourceData"];
	        this.Force = source["Force"];
	        this.PurgeCache = source["PurgeCache"];
	    }
	}
	export class ModLinkStatus {
	    entries: Record<string, string>;
	    mod_id: string;
	    damaged: number;
	
	    static createFrom(source: any = {}) {
	        return new ModLinkStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.entries = source["entries"];
	        this.mod_id = source["mod_id"];
	        this.damaged = source["damaged"];
	    }
	}
	export class UninstallOptions {
	    PurgeCache: boolean;
	    Trash: boolean;
	
	    static createFrom(source: any = {}) {
	        return new UninstallOptions(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.PurgeCache = source["PurgeCache"];
	        this.Trash = source["Trash"];
	    }
	}

}

