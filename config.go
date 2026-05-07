package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

const configFileName = "config.json"

type Config struct {
	CredentialsPath string `json:"credentialsPath"`
	OutputDir       string `json:"outputDir"`
	DeleteAfterDownload bool `json:"deleteAfterDownload"`
}

func configPath() (string, error) {
	dir, err := userConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, configFileName), nil
}

func LoadConfig() (Config, error) {
	var cfg Config
	p, err := configPath()
	if err != nil {
		return cfg, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return cfg, err
	}
	err = json.Unmarshal(b, &cfg)
	return cfg, err
}

func SaveConfig(cfg Config) error {
	p, err := configPath()
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, b, 0o644)
}
