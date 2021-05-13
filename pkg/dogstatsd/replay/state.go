// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-2020 Datadog, Inc.

package replay

import (
	"errors"
	"sync"

	"github.com/DataDog/datadog-agent/pkg/util/log"
)

var (
	mux    sync.RWMutex
	pidMap map[int32]string

	errPidMapUnavailable    = errors.New("no pid map has been set for this replay")
	errContainerUnavailable = errors.New("specified pid is not associated to any container")
)

// SetPidMap sets the map with the pid - containerID relations
func SetPidMap(m map[int32]string) {
	mux.Lock()
	defer mux.Unlock()

	pidMap = map[int32]string{}
	for pid, containerID := range m {
		pidMap[pid] = containerID
	}
}

// ContainerIDForPID returns the matching container id for a pid, or an error if not found.
func ContainerIDForPID(pid int32) (string, error) {
	mux.RLock()
	defer mux.RUnlock()

	if pidMap == nil {
		return "", errPidMapUnavailable
	}

	log.Debugf("SEARCHING for pid: %d in map: %v", pidMap)
	cID, found := pidMap[pid]
	if !found {
		return "", errContainerUnavailable
	}

	return cID, nil

}
