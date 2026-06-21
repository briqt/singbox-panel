package config

import (
	"crypto/rand"
	"encoding/hex"
	"os"
)

type Config struct {
	Port       string
	AdminToken string
	AdminUser  string
	AdminPass  string
	JWTSecret  string
	DataDir    string
	SSHKeyPath string
	TLSCert    string
	TLSKey     string
}

func Load() *Config {
	jwtSecret := getEnv("JWT_SECRET", "")
	if jwtSecret == "" {
		b := make([]byte, 32)
		rand.Read(b)
		jwtSecret = hex.EncodeToString(b)
	}
	c := &Config{
		Port:       getEnv("PORT", "8080"),
		AdminToken: getEnv("ADMIN_TOKEN", ""),
		AdminUser:  getEnv("ADMIN_USER", "admin"),
		AdminPass:  getEnv("ADMIN_PASS", ""),
		JWTSecret:  jwtSecret,
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
