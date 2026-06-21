package config

import "os"

type Config struct {
	Port       string
	AdminToken string
	DataDir    string
	SSHKeyPath string
	TLSCert    string
	TLSKey     string
}

func Load() *Config {
	c := &Config{
		Port:       getEnv("PORT", "8080"),
		AdminToken: getEnv("ADMIN_TOKEN", "changeme"),
		DataDir:    getEnv("DATA_DIR", "/opt/singbox-panel/data"),
		SSHKeyPath: getEnv("SSH_KEY_PATH", "/root/.ssh/id_ed25519"),
		TLSCert:    getEnv("TLS_CERT", ""),
		TLSKey:     getEnv("TLS_KEY", ""),
	}
	return c
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
