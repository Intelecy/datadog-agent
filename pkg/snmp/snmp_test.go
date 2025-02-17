// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2020-present Datadog, Inc.

package snmp

import (
	"testing"

	"github.com/gosnmp/gosnmp"
	"github.com/stretchr/testify/assert"
)

func TestBuildSNMPParams(t *testing.T) {
	config := Config{
		Network: "192.168.0.0/24",
	}
	_, err := config.BuildSNMPParams("192.168.0.1")
	assert.Equal(t, "No authentication mechanism specified", err.Error())

	config = Config{
		Network: "192.168.0.0/24",
		User:    "admin",
		Version: "4",
	}
	_, err = config.BuildSNMPParams("192.168.0.1")
	assert.Equal(t, "SNMP version not supported: 4", err.Error())

	config = Config{
		Network:   "192.168.0.0/24",
		Community: "public",
	}
	params, _ := config.BuildSNMPParams("192.168.0.1")
	assert.Equal(t, gosnmp.Version2c, params.Version)
	assert.Equal(t, "192.168.0.1", params.Target)

	config = Config{
		Network: "192.168.0.0/24",
		User:    "admin",
	}
	params, _ = config.BuildSNMPParams("192.168.0.2")
	assert.Equal(t, gosnmp.Version3, params.Version)
	assert.Equal(t, gosnmp.NoAuthNoPriv, params.MsgFlags)
	assert.Equal(t, "192.168.0.2", params.Target)

	config = Config{
		Network:      "192.168.0.0/24",
		User:         "admin",
		AuthProtocol: "foo",
	}
	_, err = config.BuildSNMPParams("192.168.0.1")
	assert.Equal(t, "Unsupported authentication protocol: foo", err.Error())

	config = Config{
		Network:      "192.168.0.0/24",
		User:         "admin",
		PrivProtocol: "bar",
	}
	_, err = config.BuildSNMPParams("192.168.0.1")
	assert.Equal(t, "Unsupported privacy protocol: bar", err.Error())
}
