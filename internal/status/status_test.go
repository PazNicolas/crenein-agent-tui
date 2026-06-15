package status_test

import (
	"fmt"
	"testing"

	"github.com/PazNicolas/crenein-agent-tui/internal/status"
)

// validComposeContent is a minimal docker-compose.yml used by parser tests.
const validComposeContent = `
version: "3"
services:
  agent:
    image: crenein/c-network-agent-back:1.8.3
  frontend:
    image: crenein/c-network-agent-front:1.8.3
  mongodb:
    image: mongodb/mongodb-community-server:7.0-ubi8
  influxdb:
    image: influxdb:2.7
  redis:
    image: redis:7.2
`

// ─── ParseUptimeSeconds ───────────────────────────────────────────────────────

func TestParseUptimeSeconds(t *testing.T) {
	cases := []struct {
		statusStr string
		want      int64
	}{
		{"Up 3 days", 3 * 86400},
		{"Up 2 minutes", 2 * 60},
		{"Up About an hour", 3600},
		{"Up 1 hour", 3600},
		{"Up 2 hours", 2 * 3600},
		{"Up 5 weeks", 5 * 7 * 86400},
		{"Up 3 days (healthy)", 3 * 86400},
		{"Up 3 days (unhealthy)", 3 * 86400},
		{"Exited (0) 2 hours ago", 0},
		{"", 0},
		{"Up", 0},
	}

	for _, tc := range cases {
		t.Run(tc.statusStr, func(t *testing.T) {
			got := status.ParseUptimeSeconds(tc.statusStr)
			if got != tc.want {
				t.Errorf("ParseUptimeSeconds(%q) = %d, want %d", tc.statusStr, got, tc.want)
			}
		})
	}
}

// ─── ParseHealthFromStatus ────────────────────────────────────────────────────

func TestParseHealthFromStatus(t *testing.T) {
	cases := []struct {
		statusStr string
		want      string
	}{
		{"Up 3 days (healthy)", "healthy"},
		{"Up 3 days (unhealthy)", "unhealthy"},
		{"Up 3 days", "none"},
		{"Exited (0)", "none"},
	}
	for _, tc := range cases {
		t.Run(tc.statusStr, func(t *testing.T) {
			got := status.ParseHealthFromStatus(tc.statusStr)
			if got != tc.want {
				t.Errorf("ParseHealthFromStatus(%q) = %q, want %q", tc.statusStr, got, tc.want)
			}
		})
	}
}

// ─── ImageTagFromCompose ──────────────────────────────────────────────────────

func TestImageTagFromCompose(t *testing.T) {
	cases := []struct {
		service string
		want    string
	}{
		{"agent", "crenein/c-network-agent-back:1.8.3"},
		{"mongodb", "mongodb/mongodb-community-server:7.0-ubi8"},
		{"redis", "redis:7.2"},
		{"nonexistent", ""},
	}
	for _, tc := range cases {
		t.Run(tc.service, func(t *testing.T) {
			got := status.ImageTagFromCompose(validComposeContent, tc.service)
			if got != tc.want {
				t.Errorf("ImageTagFromCompose(_, %q) = %q, want %q", tc.service, got, tc.want)
			}
		})
	}
}

// ─── MongoInfoFromCompose ─────────────────────────────────────────────────────

func TestMongoInfoFromCompose(t *testing.T) {
	imageTag, major := status.MongoInfoFromCompose(validComposeContent)
	if imageTag != "mongodb/mongodb-community-server:7.0-ubi8" {
		t.Errorf("imageTag = %q, want mongodb/mongodb-community-server:7.0-ubi8", imageTag)
	}
	if major != "7.x" {
		t.Errorf("major = %q, want 7.x", major)
	}
}

// ─── ContainerStateFromStatus ─────────────────────────────────────────────────

func TestContainerStateFromStatus(t *testing.T) {
	cases := []struct {
		statusStr string
		running   bool
		want      string
	}{
		{"Up 3 days", true, "running"},
		{"Up 3 days (healthy)", true, "running"},
		{"Exited (0) 2 hours ago", false, "exited"},
		{"Restarting (1) 30 seconds ago", false, "restarting"},
		{"Created", false, "created"},
		{"Paused", false, "paused"},
		{"", false, "exited"},
		{"", true, "running"},
	}
	for _, tc := range cases {
		t.Run(tc.statusStr+"_running="+fmt.Sprintf("%v", tc.running), func(t *testing.T) {
			got := status.ContainerStateFromStatus(tc.statusStr, tc.running)
			if got != tc.want {
				t.Errorf("ContainerStateFromStatus(%q, %v) = %q, want %q", tc.statusStr, tc.running, got, tc.want)
			}
		})
	}
}
