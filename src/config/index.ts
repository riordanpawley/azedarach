/**
 * Configuration Module
 *
 * Exports all configuration-related types, schemas, and services.
 */

// Schema and types
export {
  AzedarachConfigSchema,
  type AzedarachConfig,
  type AzedarachConfigInput,
  type WorktreeConfig,
  type SessionConfig,
  type PatternsConfig,
  type PRConfig,
  type NotificationsConfig,
} from "./schema.js"

// Defaults and resolved types
export { DEFAULT_CONFIG, mergeWithDefaults, type ResolvedConfig } from "./defaults.js"

// Service and layers
export {
  // Service
  AppConfig,
  type AppConfigService,
  // Errors
  ConfigError,
  ConfigParseError,
  // Layers
  AppConfigLive,
  AppConfigLiveWithPlatform,
  // Loader
  loadConfig,
  // Convenience functions
  getWorktreeConfig,
  getSessionConfig,
  getPatternsConfig,
  getPRConfig,
  getNotificationsConfig,
} from "./AppConfig.js"
