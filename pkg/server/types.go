package server

// StatusResponse represents a simple status response
type StatusResponse struct {
	Status string `json:"status" example:"ok"`
}

// VersionResponse represents version information
type VersionResponse struct {
	Version     string `json:"version" example:"v1.0.0"`
	Commit      string `json:"commit" example:"abc1234567890"`
	CommitShort string `json:"commit_short" example:"abc1234"`
	BuildTime   string `json:"build_time" example:"2024-01-01T00:00:00Z"`
}

// ErrorResponse represents an error response
type ErrorResponse struct {
	Level   string `json:"level" example:"error"`
	Message string `json:"message" example:"error log generated"`
	Error   string `json:"error,omitempty" example:"test error"`
}

// DelayResponse represents a delay response
type DelayResponse struct {
	Delay string `json:"delay" example:"5"`
}

// ConfigsResponse represents the config watcher response
type ConfigsResponse struct {
	Enabled bool              `json:"enabled" example:"true"`
	Path    string            `json:"path,omitempty" example:"/etc/config"`
	Configs map[string]string `json:"configs,omitempty"`
	Message string            `json:"message,omitempty"`
}
