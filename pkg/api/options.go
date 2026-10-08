package api

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ChocolateNao/darktide-mod-manager/internal/logging"
	"github.com/ChocolateNao/darktide-mod-manager/pkg/domain"
	appErrors "github.com/ChocolateNao/darktide-mod-manager/pkg/errors"
)

// Logger is the minimum a caller has to provide to get manager output. It is
// satisfied by *log.Logger, by slog's adapters in the standard library, and by
// anything else with a Printf method.
type Logger interface {
	Printf(format string, args ...any)
}

// LoggerFunc adapts a plain function to Logger, so a caller that wants to
// collect manager output in a slice, or write it somewhere other than the
// standard logger, does not have to declare a type for it.
//
// A nil LoggerFunc is usable and discards: WithLogger already drops a nil
// logger, and the alternative — a nil-receiver panic on the first diagnostic —
// would turn a missing option into a crash in the middle of start-up.
type LoggerFunc func(format string, args ...any)

// Printf implements Logger.
func (f LoggerFunc) Printf(format string, args ...any) {
	if f == nil {
		return
	}

	f(format, args...)
}

// settings is what the options accumulate. It is unexported because an Option
// is a function over an unexported type: that is what lets a future release add
// a new knob without changing every existing Option's signature, which is the
// whole point of the pattern.
type settings struct {
	logger              Logger
	sourceConfigs       map[string]domain.SourceConfig
	sourceEnabledStated map[string]bool
	logging             *logging.Service
	configPath          string
	gamePath            string
	cachePath           string
	storagePath         string
	handlers            []EventHandler
	loggingOpts         LoggingOptions
}

// WithLogging supplies a log service that wire() adopts instead of building one.
//
// The caller keeps constructing it: this option only decides who owns the
// configuration. Ownership of the *lifetime* still belongs to the returned
// bundle, which closes the service with its other resources, because a log sink
// that outlives the services that write to it loses the lines they were about to
// write.
func WithLogging(service *logging.Service) Option {
	return func(s *settings) error {
		if service == nil {
			return nil
		}

		s.logging = service

		return nil
	}
}

// WithLoggingOptions sets the per-invocation logging overrides (--log-level,
// --log-dir, --log-console, --no-log-file, extra redaction fields).
func WithLoggingOptions(opts LoggingOptions) Option {
	return func(s *settings) error {
		s.loggingOpts = opts
		return nil
	}
}

// Option configures a client before anything is opened.
//
// The type takes a *settings rather than the *Client the document sketches. The
// difference matters: an option that needs to validate its argument, or that has
// to run before the first file is opened, cannot do either if it is handed a
// half-built client. Returning an error keeps a mistyped option a construction
// failure instead of a confusing failure on the first mod install.
type Option func(*settings) error

// normalizeSourceID matches the registry's own key rule so that an option naming
// a source in a different case configures the same one.
func normalizeSourceID(id string) string {
	return strings.ToLower(strings.TrimSpace(id))
}

// applyOptions runs the options in order and returns the accumulated settings.
// A nil Option in the list is skipped rather than panicking, because a caller
// assembling options from a slice with a conditional hole should not crash.
func applyOptions(opts []Option) (*settings, error) {
	s := &settings{
		sourceConfigs:       make(map[string]domain.SourceConfig),
		sourceEnabledStated: make(map[string]bool),
	}

	for _, opt := range opts {
		if opt == nil {
			continue
		}

		if err := opt(s); err != nil {
			return nil, err
		}
	}

	return s, nil
}

// WithConfigPath reads and writes the manager's configuration at path instead of
// the platform default. The file need not exist: a missing file yields defaults
// on first load, which is what makes a fresh install work.
func WithConfigPath(path string) Option {
	return func(s *settings) error {
		trimmed := strings.TrimSpace(path)
		if trimmed == "" {
			return appErrors.New(appErrors.ErrCodeInvalidArgument,
				"config path must not be blank")
		}

		s.configPath = trimmed

		return nil
	}
}

// WithGamePath points the manager at a specific Darktide installation.
//
// The path is validated before it is stored, and the configuration is written
// so a later start-up reuses it. That is a side effect of an option that looks
// like a value assignment, and it is deliberate: the alternative is a game path
// that only lasts for the lifetime of the client, so the CLI would need the same
// path on every invocation.
func WithGamePath(path string) Option {
	return func(s *settings) error {
		trimmed := strings.TrimSpace(path)
		if trimmed == "" {
			return appErrors.New(appErrors.ErrCodeInvalidArgument,
				"game path must not be blank")
		}

		s.gamePath = trimmed

		return nil
	}
}

// WithCachePath stores downloaded archives under path instead of the platform
// cache directory.
func WithCachePath(path string) Option {
	return func(s *settings) error {
		trimmed := strings.TrimSpace(path)
		if trimmed == "" {
			return appErrors.New(appErrors.ErrCodeInvalidArgument,
				"cache path must not be blank")
		}

		s.cachePath = trimmed

		return nil
	}
}

// WithStoragePath stores mod payloads under path instead of the configured or
// platform default.
func WithStoragePath(path string) Option {
	return func(s *settings) error {
		trimmed := strings.TrimSpace(path)
		if trimmed == "" {
			return appErrors.New(appErrors.ErrCodeInvalidArgument,
				"storage path must not be blank")
		}

		s.storagePath = trimmed

		return nil
	}
}

// WithSourceConfig configures one mod source.
//
// The block is a plain map so a caller can build it from JSON without importing
// anything from this package. Four keys are structural and every other key is
// handed to the source as a provider option:
//
//	enabled  bool or "true"/"false"/"1"/"0"/"yes"/"no" — a source is enabled
//	         unless this says otherwise
//	api_key  string — the source's credential
//	paths    string list — only meaningful to the local source
//
// Everything else becomes an option the source itself reads, which is how
// `{"api_base": ...}` or `{"repositories": [...]}` reach a plugin without this
// package having to know every provider's private keys. A list of scalars is
// joined with commas, because the providers split their string options on
// commas — so a value read back from the configuration file and a value passed
// here behave identically.
//
// Unknown keys are *not* rejected. Enumerating them here would make adding a
// plugin option a breaking change to this package, and a typo lands as an
// option the plugin ignores, which is the same outcome the configuration file
// already has.
func WithSourceConfig(sourceID string, cfg map[string]any) Option {
	return func(s *settings) error {
		id := normalizeSourceID(sourceID)
		if id == "" {
			return appErrors.New(appErrors.ErrCodeInvalidArgument,
				"source id must not be blank")
		}

		if s.sourceConfigs == nil {
			s.sourceConfigs = make(map[string]domain.SourceConfig)
		}

		block, enabledStated, err := sourceConfigFrom(id, cfg)
		if err != nil {
			return err
		}
		// A later call for the same source merges over the earlier one rather
		// than replacing it wholesale, so a caller can set a token and then
		// adjust one option without repeating the first.
		//
		// A source nobody has configured yet starts enabled, which is the same
		// rule the registry applies to a missing block: a manager that lists no
		// sources on a fresh install looks broken. Naming a source in an option is
		// a statement that the caller wants it, not a statement that it is off.
		existing, seen := s.sourceConfigs[id]
		if !seen {
			existing = domain.SourceConfig{Enabled: true}
		}

		s.sourceConfigs[id] = mergeSourceBlock(existing, block, enabledStated)
		// "Stated" is sticky. A later block for the same source that mentions only
		// a token has not retracted an earlier explicit disable, and recording
		// false here would let the wiring treat that disable as never stated and
		// switch the source back on.
		if enabledStated {
			s.sourceEnabledStated[id] = true
		}

		return nil
	}
}

// mergeSourceBlock folds an override onto an existing block.
//
// enabledStated is the whole reason this function takes three arguments. A bool
// field cannot distinguish "left alone" from "set to false", and a source block
// from a caller that mentioned only a token must not decide whether the source
// is enabled. Every other field merges the same way: a non-empty override wins,
// a list is appended to rather than replacing, so a caller can add one path
// without restating the others.
func mergeSourceBlock(
	existing, override domain.SourceConfig,
	enabledStated bool,
) domain.SourceConfig {
	merged := existing
	if enabledStated {
		merged.Enabled = override.Enabled
	}

	if override.APIKey != "" {
		merged.APIKey = override.APIKey
	}

	if len(override.Paths) > 0 {
		merged.Paths = append(append([]string(nil), existing.Paths...), override.Paths...)
	}
	// The options map is always rebuilt rather than written through. `existing` is a
	// struct copy, so its map is the caller's; writing into it would silently edit
	// the block that was passed in — which, in the wiring, is a block that came
	// straight out of the loaded configuration file.
	if len(existing.Options) > 0 || len(override.Options) > 0 {
		merged.Options = make(map[string]string, len(existing.Options)+len(override.Options))
		for k, v := range existing.Options {
			merged.Options[k] = v
		}

		for k, v := range override.Options {
			merged.Options[k] = v
		}
	} else {
		merged.Options = nil
	}

	return merged
}

// sourceConfigFrom converts a caller's map into the domain block. It is
// separated from the option so the conversion is testable on its own.
//
// The second result reports whether the block actually mentioned `enabled`; see
// mergeSourceBlock for why the domain type cannot carry that itself.
func sourceConfigFrom(id string, cfg map[string]any) (domain.SourceConfig, bool, error) {
	block := domain.SourceConfig{Enabled: true}

	enabledStated := false
	if len(cfg) == 0 {
		return block, enabledStated, nil
	}

	keys := make([]string, 0, len(cfg))
	for k := range cfg {
		keys = append(keys, k)
	}

	sort.Strings(keys)

	for _, key := range keys {
		value := cfg[key]
		switch normalizeKey(key) {
		case "enabled":
			parsed, ok := parseBool(value)
			if !ok {
				return domain.SourceConfig{}, false, appErrors.New(appErrors.ErrCodeInvalidArgument,
					"source %q: enabled must be a boolean, got %T", id, value)
			}

			block.Enabled = parsed
			enabledStated = true
		case "apikey", "key", "token":
			s, ok := scalarString(value)
			if !ok {
				return domain.SourceConfig{}, false, appErrors.New(appErrors.ErrCodeInvalidArgument,
					"source %q: %s must be a string", id, key)
			}

			block.APIKey = s
		case "paths":
			list, err := stringList(value)
			if err != nil {
				return domain.SourceConfig{}, false, appErrors.New(appErrors.ErrCodeInvalidArgument,
					"source %q: %s %s", id, key, err)
			}

			block.Paths = list
		case "options":
			// The nested form mirrors domain.SourceConfig, so a caller can hand
			// over a block decoded straight from JSON. It is also the only key
			// whose value is a map.
			nested, err := optionMap(value)
			if err != nil {
				return domain.SourceConfig{}, false, appErrors.New(appErrors.ErrCodeInvalidArgument,
					"source %q: options %s", id, err)
			}

			if block.Options == nil {
				block.Options = make(map[string]string, len(nested))
			}

			for k, v := range nested {
				block.Options[k] = v
			}
		default:
			s, err := optionString(value)
			if err != nil {
				return domain.SourceConfig{}, false, appErrors.New(appErrors.ErrCodeInvalidArgument,
					"source %q: %s %s", id, key, err)
			}

			if block.Options == nil {
				block.Options = make(map[string]string)
			}
			// The key is stored verbatim rather than folded. The providers read
			// their own option names — api_base, game_domain, timeout_seconds,
			// repositories — and folding separators away would rename every one
			// of them. Only the structural keys above are matched loosely.
			block.Options[strings.TrimSpace(key)] = s
		}
	}

	return block, enabledStated, nil
}

// optionMap reads the nested `options` object, refusing a value that is not a map
// of scalars.
func optionMap(value any) (map[string]string, error) {
	if value == nil {
		return nil, nil
	}

	raw, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("must be an object of strings, got %T", value)
	}

	out := make(map[string]string, len(raw))
	for k, v := range raw {
		s, ok := scalarString(v)
		if !ok {
			return nil, fmt.Errorf("entry %q must be a string, got %T", k, v)
		}

		trimmed := strings.TrimSpace(k)
		if trimmed != "" {
			out[trimmed] = s
		}
	}

	return out, nil
}

// normalizeKey folds a key for **matching a structural setting** only: `api_key`,
// `apiKey` and `API-Key` all name the credential. Provider option names are
// stored verbatim — see sourceConfigFrom — because the plugins own those
// spellings and folding them would rename every one.
func normalizeKey(key string) string {
	var b strings.Builder

	for _, r := range strings.TrimSpace(key) {
		switch r {
		case '_', '-', ' ', '.':
			continue
		default:
			b.WriteRune(r)
		}
	}

	return strings.ToLower(b.String())
}

// parseBool accepts the shapes a configuration file or a JSON document can hand
// over for a boolean. A string is accepted because a config editor that writes
// "false" as a quoted string is a normal thing to encounter, and refusing it
// would fail construction for a value the user can see plainly.
func parseBool(value any) (bool, bool) {
	switch v := value.(type) {
	case bool:
		return v, true
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "true", "yes", "on", "1":
			return true, true
		case "false", "no", "off", "0":
			return false, true
		}
	case nil:
		return false, false
	}

	return false, false
}

// scalarString renders a single value as a string, refusing containers.
func scalarString(value any) (string, bool) {
	switch v := value.(type) {
	case nil:
		return "", true
	case string:
		return strings.TrimSpace(v), true
	case bool, int, int64, float64, float32:
		return fmt.Sprint(v), true
	}

	return "", false
}

// optionString renders a provider option value: either one scalar or a list of
// them, the list joined with commas because that is what the plugins split on.
func optionString(value any) (string, error) {
	if list, ok := value.([]any); ok {
		parts := make([]string, 0, len(list))
		for _, item := range list {
			s, ok := scalarString(item)
			if !ok {
				return "", fmt.Errorf("contains a %T, which is not a string", item)
			}

			if s != "" {
				parts = append(parts, s)
			}
		}

		return strings.Join(parts, ","), nil
	}

	if list, ok := value.([]string); ok {
		parts := make([]string, 0, len(list))
		for _, item := range list {
			if trimmed := strings.TrimSpace(item); trimmed != "" {
				parts = append(parts, trimmed)
			}
		}

		return strings.Join(parts, ","), nil
	}

	s, ok := scalarString(value)
	if !ok {
		return "", fmt.Errorf("must be a string or a list of strings, got %T", value)
	}

	return s, nil
}

// stringList reads the `paths` key. A single string is split on commas and
// newlines, so a caller that passes a single path and a caller that passes a
// list get the same block.
func stringList(value any) ([]string, error) {
	switch v := value.(type) {
	case nil:
		return nil, nil
	case []string:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if trimmed := strings.TrimSpace(item); trimmed != "" {
				out = append(out, trimmed)
			}
		}

		return out, nil
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			s, ok := scalarString(item)
			if !ok {
				return nil, fmt.Errorf("contains a %T, which is not a path", item)
			}

			if s != "" {
				out = append(out, s)
			}
		}

		return out, nil
	case string:
		out := make([]string, 0, 4)

		for _, field := range strings.FieldsFunc(v, func(r rune) bool {
			return r == ',' || r == '\n' || r == '\r'
		}) {
			if trimmed := strings.TrimSpace(field); trimmed != "" {
				out = append(out, trimmed)
			}
		}

		return out, nil
	}

	s, ok := scalarString(value)
	if !ok {
		return nil, fmt.Errorf("must be a string or a list of strings, got %T", value)
	}

	if s == "" {
		return nil, nil
	}

	return []string{s}, nil
}

// WithEventHandler subscribes a handler to the client's event bus.
//
// Handlers are subscribed before anything is wired, so the very first event of a
// client's life — a source being registered, a mod install starting — is
// delivered. A nil handler is dropped rather than refused, so a caller can pass
// one unconditionally.
func WithEventHandler(handler EventHandler) Option {
	return func(s *settings) error {
		if handler == nil {
			return nil
		}

		s.handlers = append(s.handlers, handler)

		return nil
	}
}

// WithLogger routes manager output to logger. A nil logger keeps the default,
// which discards everything; the manager is a library first and printing to
// standard output from someone else's program would be rude.
func WithLogger(logger Logger) Option {
	return func(s *settings) error {
		if logger == nil {
			return nil
		}

		s.logger = logger

		return nil
	}
}
