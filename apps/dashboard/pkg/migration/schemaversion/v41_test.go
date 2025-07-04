package schemaversion_test

import (
	"testing"

	"github.com/grafana/grafana/apps/dashboard/pkg/migration/schemaversion"
)

func TestV41(t *testing.T) {
	tests := []migrationTestCase{
		{
			name: "time_options is removed",
			input: map[string]interface{}{
				"title": "Test Dashboard",
				"timepicker": map[string]interface{}{
					"time_options": []string{"1m", "5m", "15m", "1h", "6h", "12h", "24h"},
				},
			},
			expected: map[string]interface{}{
				"title":         "Test Dashboard",
				"schemaVersion": 41,
				"timepicker":    map[string]interface{}{},
			},
		},
		{
			name: "timepicker without time_options is unchanged",
			input: map[string]interface{}{
				"title": "Test Dashboard",
				"timepicker": map[string]interface{}{
					"refresh_intervals": []string{"5s", "10s", "30s", "1m", "5m", "15m", "30m", "1h", "2h", "1d"},
				},
			},
			expected: map[string]interface{}{
				"title":         "Test Dashboard",
				"schemaVersion": 41,
				"timepicker": map[string]interface{}{
					"refresh_intervals": []string{"5s", "10s", "30s", "1m", "5m", "15m", "30m", "1h", "2h", "1d"},
				},
			},
		},
		{
			name: "timepicker is not set",
			input: map[string]interface{}{
				"title": "Test Dashboard",
			},
			expected: map[string]interface{}{
				"title":         "Test Dashboard",
				"schemaVersion": 41,
			},
		},
	}

	runMigrationTests(t, tests, schemaversion.V41)
}
