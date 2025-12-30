package config

type Config struct {
	CLITool string
}

func Load() (*Config, error) {
	return &Config{
		CLITool: "claude",
	}, nil
}
