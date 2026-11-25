package config

import (
	"log/slog"
	"testing"
	"time"
)

func lookupFrom(m map[string]string) LookupFunc {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

func TestLoad(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		check   func(t *testing.T, c Config)
		wantErr bool
	}{
		{
			name: "defaults",
			env:  map[string]string{},
			check: func(t *testing.T, c Config) {
				if c.HTTPAddr != ":8080" || c.DatabaseURL != DefaultDatabaseURL || c.LogLevel != slog.LevelInfo ||
					c.ShutdownTimeout != 15*time.Second || !c.AutoMigrate || c.AuthMode != AuthModeDev {
					t.Fatalf("unexpected defaults: %+v", c)
				}
			},
		},
		{
			name: "overrides",
			env: map[string]string{
				"IACP_HTTP_ADDR": ":9090", "IACP_LOG_LEVEL": "debug", "IACP_SHUTDOWN_TIMEOUT": "3s",
				"IACP_AUTO_MIGRATE": "false", "IACP_DATABASE_URL": "postgres://x",
			},
			check: func(t *testing.T, c Config) {
				if c.HTTPAddr != ":9090" || c.LogLevel != slog.LevelDebug || c.ShutdownTimeout != 3*time.Second ||
					c.AutoMigrate || c.DatabaseURL != "postgres://x" {
					t.Fatalf("overrides not applied: %+v", c)
				}
			},
		},
		{name: "bad level", env: map[string]string{"IACP_LOG_LEVEL": "loud"}, wantErr: true},
		{name: "bad timeout", env: map[string]string{"IACP_SHUTDOWN_TIMEOUT": "soon"}, wantErr: true},
		{name: "bad auth mode", env: map[string]string{"IACP_AUTH_MODE": "none"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := Load(lookupFrom(tt.env))
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.check != nil {
				tt.check(t, c)
			}
		})
	}
}
