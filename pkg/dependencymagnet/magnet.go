package depenencymagnet

import (
	// This is required to build both the Readiness Probe and Version Upgrade Hook.
	// See docker/mongodb-enterprise-init-database/Dockerfile.builder
	_ "gopkg.in/natefinch/lumberjack.v2"
)
