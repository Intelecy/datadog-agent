// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

package replay

import (
	"sync"

	"github.com/DataDog/datadog-agent/pkg/tagger/telemetry"
	"github.com/DataDog/datadog-agent/pkg/tagger/types"
	"github.com/DataDog/datadog-agent/pkg/util/containers"
)

const replaySource = "replay"

type tagStore struct {
	mutex     sync.RWMutex
	store     map[string]*types.Entity
	telemetry map[string]float64
}

func newTagStore() *tagStore {
	return &tagStore{
		store:     make(map[string]*types.Entity),
		telemetry: make(map[string]float64),
	}
}

func (s *tagStore) addEntity(entityID string, e types.Entity) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.store[entityID] = &e

	return nil
}

func (s *tagStore) getEntity(entityID string) (*types.Entity, bool) {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	entity, ok := s.store[entityID]
	return entity, ok
}

func (s *tagStore) listEntities() []*types.Entity {
	s.mutex.RLock()
	defer s.mutex.RUnlock()
	entities := make([]*types.Entity, 0, len(s.store))

	for _, e := range s.store {
		entities = append(entities, e)
	}

	return entities
}

func (s *tagStore) collectTelemetry() {
	// our telemetry package does not seem to have a way to reset a Gauge,
	// so we need to keep track of all the labels we use, and re-set them
	// to zero after we're done to ensure a new run of collectTelemetry
	// will not forget to clear them if they disappear.

	s.mutex.Lock()
	defer s.mutex.Unlock()

	for _, entity := range s.store {
		prefix, _ := containers.SplitEntityName(entity.ID)
		s.telemetry[prefix]++
	}

	for prefix, storedEntities := range s.telemetry {
		telemetry.StoredEntities.Set(storedEntities, replaySource, prefix)
		s.telemetry[prefix] = 0
	}
}

// reset clears the local store, preparing it to be re-initialized from a fresh
// NOTE: caller must ensure that it holds s.mutex's lock, as this func does not
// do it on its own.
func (s *tagStore) reset() {
	if len(s.store) == 0 {
		return
	}

	s.store = make(map[string]*types.Entity)
}
