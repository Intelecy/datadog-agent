// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

package compliance

import (
	"github.com/DataDog/datadog-agent/pkg/compliance/event"
)

// Report contains the result of a compliance check
type Report struct {
	// Data contains arbitrary data linked to check evaluation
	Data event.Data
	// Passed defines whether check was successful or not
	Passed bool
	// Error of th check evaluation
	Error error
}
