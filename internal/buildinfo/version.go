package buildinfo

// Version is set at build time via -ldflags.
// Default value is used for local/dev builds.
var Version = "dev"
