package main

import (
	"errors"
	"fmt"
	"testing"

	"github.com/openchat/openchat/server/db/mysql"
)

func TestSchemaInitializationFatal(t *testing.T) {
	for _, environment := range []string{"", "test", "development", "production"} {
		t.Run("APP_ENV="+environment, func(t *testing.T) {
			for _, key := range []string{"OC_ENV", "APP_ENV", "GO_ENV", "ENV"} {
				t.Setenv(key, "")
			}
			t.Setenv("APP_ENV", environment)
			for _, tc := range []struct {
				name string
				err  error
				want bool
			}{
				{"success", nil, false},
				{"unrelated schema error", errors.New("ordinary schema error"), environment == "production"},
				{"fork schema error", mysql.ErrSessionForkSchema, true},
				{"wrapped fork schema error", fmt.Errorf("initialize: %w", mysql.ErrSessionForkSchema), true},
			} {
				t.Run(tc.name, func(t *testing.T) {
					if got := schemaInitializationFatal(tc.err); got != tc.want {
						t.Fatalf("fatal = %v, want %v for %v", got, tc.want, tc.err)
					}
				})
			}
		})
	}
}
