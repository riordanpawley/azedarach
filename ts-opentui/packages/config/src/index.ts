/**
 * Configuration Module
 *
 * Exports all configuration-related types, schemas, and services.
 */

// Service and layers
export {
	// Service
	AppConfig,
	AppConfigConfig,
	type AppConfigService,
	// Errors
	ConfigError,
	ConfigParseError,
} from "./AppConfig.js"

// Defaults and resolved types
export { DEFAULT_CONFIG, mergeWithDefaults, type ResolvedConfig } from "./defaults.js"
export {
	AZEDARACH_STORAGE_DIRECTORY,
	getProjectStoragePaths,
	type ProjectStoragePaths,
	resolveConfigBasePath,
	resolveConfigSchemaPath,
} from "./projectPaths.js"
// Schema and types
export {
	AZEDARACH_CONFIG_JSON_SCHEMA_URI,
	type AzedarachConfig,
	type AzedarachConfigInput,
	AzedarachConfigJsonSchema,
	AzedarachConfigSchema,
	type CliTool,
	type DevServerConfig,
	type HooksConfig,
	type NotificationsConfig,
	type PatternsConfig,
	type PRConfig,
	type SessionConfig,
	type SpecConfig,
	type WorkflowMode,
	type WorktreeConfig,
} from "./schema.js"
