package repair

import (
	"context"
	"database/sql"
	"errors"

	"github.com/Cyaside/Triovexa/internal/coderepair"
)

func deploymentContractTx(ctx context.Context, tx *sql.Tx, c coderepair.Case, environment string) (*coderepair.TelemetryContract, error) {
	binding, err := getRepairBindingTx(ctx, tx, c.BindingID)
	if err != nil {
		return nil, err
	}
	digest, err := coderepair.ScopeDigest(c, binding)
	if err != nil || !binding.Enabled || binding.Environment != environment || digest != c.ScopeDigest {
		return nil, errors.New("deployment binding scope changed")
	}
	contract := binding.TelemetryContract()
	if contract != nil && contract.Validate() != nil {
		return nil, errors.New("deployment telemetry profile is invalid")
	}
	return contract, nil
}
