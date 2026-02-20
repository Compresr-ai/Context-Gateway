package unit

import (
	"testing"

	"github.com/compresr/context-gateway/internal/costcontrol"
	"github.com/stretchr/testify/assert"
)

func TestCostControlConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		config  costcontrol.CostControlConfig
		wantErr bool
	}{
		{
			name:    "disabled config is valid",
			config:  costcontrol.CostControlConfig{Enabled: false, SessionCap: 0},
			wantErr: false,
		},
		{
			name:    "enabled with valid cap",
			config:  costcontrol.CostControlConfig{Enabled: true, SessionCap: 5.0},
			wantErr: false,
		},
		{
			name:    "enabled with zero cap (unlimited)",
			config:  costcontrol.CostControlConfig{Enabled: true, SessionCap: 0},
			wantErr: false,
		},
		{
			name:    "negative session cap is invalid",
			config:  costcontrol.CostControlConfig{Enabled: true, SessionCap: -1.0},
			wantErr: true,
		},
		{
			name:    "negative global cap is invalid",
			config:  costcontrol.CostControlConfig{Enabled: true, GlobalCap: -1.0},
			wantErr: true,
		},
		{
			name:    "valid global cap",
			config:  costcontrol.CostControlConfig{Enabled: true, GlobalCap: 10.0},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
