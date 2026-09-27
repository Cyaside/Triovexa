package workload

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// checkRepairFixture is an opt-in staging fixture. A worker built from this
// revision cannot understand schema 2, so restarting it cannot clear the
// failure. The regression test under the repair_regression build tag describes
// the expected code change without breaking the normal development test run.
func checkRepairFixture(schema, appEnv string) error {
	if strings.TrimSpace(schema) == "" {
		return nil
	}
	if strings.ToLower(strings.TrimSpace(appEnv)) != "local" {
		return errors.New("repair fixture is restricted to the local environment")
	}
	version, err := strconv.Atoi(schema)
	if err != nil {
		return fmt.Errorf("repair fixture schema: %w", err)
	}
	if version != 1 {
		return fmt.Errorf("repair fixture: unsupported job schema version %d", version)
	}
	return nil
}
