package config

import (
	"crypto/rand"
	"encoding/hex"
	"log"
	"os"
	"time"
)

type Config struct {
	Port       string
	AdminUser  string
	AdminPass  string
	JWTSecret  string
	DataDir    string
	SSHKeyPath string
	TLSCert    string
	TLSKey     string
	// Location bounds every calendar decision the panel makes: which day a
	// traffic sample belongs to and where the retention window starts.
	// Samples are stored in UTC, so this is the only place the two differ.
	Location *time.Location
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
		AdminUser:  getEnv("ADMIN_USER", "admin"),
		AdminPass:  getEnv("ADMIN_PASS", ""),
		JWTSecret:  jwtSecret,
		DataDir:    getEnv("DATA_DIR", "/opt/singbox-panel/data"),
		SSHKeyPath: getEnv("SSH_KEY_PATH", "/root/.ssh/id_ed25519"),
		TLSCert:    getEnv("TLS_CERT", ""),
		TLSKey:     getEnv("TLS_KEY", ""),
		Location:   loadLocation(getEnv("TIMEZONE", "Asia/Shanghai")),
	}
	return c
}

func loadLocation(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		log.Printf("config: unknown TIMEZONE %q, falling back to UTC: %v", name, err)
		return time.UTC
	}
	return loc
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
