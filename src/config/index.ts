/**
 * Configuration Module
 *
 * Exports all configuration-related types, schemas, and services.
 */

// Service and layers
export {
	// Service
	AppConfig,
	type AppConfigService,
	// Errors
	ConfigError,
	ConfigParseError,
} from "./AppConfig.js"

// Defaults and resolved types
export { DEFAULT_CONFIG, mergeWithDefaults, type ResolvedConfig } from "./defaults.js"
// Schema and types
export {
	type AzedarachConfig,
	type AzedarachConfigInput,
	AzedarachConfigSchema,
	type DevServerConfig,
	type NotificationsConfig,
	type PatternsConfig,
	type PortConfig,
	type PRConfig,
	type SessionConfig,
	type WorktreeConfig,
} from "./schema.js"
