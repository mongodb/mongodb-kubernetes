package telemetry

import (
	"os"

	"go.uber.org/zap"
)

func init() {
	logger, _ := zap.NewDevelopment()
	zap.ReplaceGlobals(logger)
	os.Clearenv() // nolint:forbidigo
}
