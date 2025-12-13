/**
 * Configuration Module
 *
 * Exports all configuration-related types, schemas, and services.
 */

// Service and layers
export {
	// Service
	AppConfig,
	// Layers
	AppConfigLive,
	AppConfigLiveWithPlatform,
	type AppConfigService,
	// Errors
	ConfigError,
	ConfigParseError,
	getNotificationsConfig,
	getPatternsConfig,
	getPRConfig,
	getSessionConfig,
	// Convenience functions
	getWorktreeConfig,
	// Loader
	loadConfig,
} from "./AppConfig.js"

// Defaults and resolved types
export { DEFAULT_CONFIG, mergeWithDefaults, type ResolvedConfig } from "./defaults.js"
// Schema and types
export {
	type AzedarachConfig,
	type AzedarachConfigInput,
	AzedarachConfigSchema,
	type NotificationsConfig,
	type PatternsConfig,
	type PRConfig,
	type SessionConfig,
	type WorktreeConfig,
} from "./schema.js"
