package module

import (
	"fmt"
	"sync"

	"github.com/DataDog/datadog-agent/cmd/system-probe/config"
	"github.com/DataDog/datadog-agent/pkg/util/log"
	"github.com/gorilla/mux"
	"github.com/pkg/errors"
)

var l *loader

func init() {
	l = &loader{
		modules: make(map[config.ModuleName]Module),
	}
}

// loader is responsible for managing the lifecyle of each api.Module, which includes:
// * Module initialization;
// * Module termination;
// * Module telemetry consolidation;
type loader struct {
	sync.Mutex
	modules map[config.ModuleName]Module
	cfg     *config.Config
	httpMux *mux.Router
	closed  bool
}

// Register a set of modules, which involves:
// * Initialization using the provided Factory;
// * Registering the HTTP endpoints of each module;
func Register(cfg *config.Config, httpMux *mux.Router, factories []Factory) error {
	for _, factory := range factories {
		if !cfg.ModuleIsEnabled(factory.Name) {
			log.Infof("%s module disabled", factory.Name)
			continue
		}

		module, err := factory.Fn(cfg)

		// In case a module failed to be started, do not make the whole `system-probe` abort.
		// Let `system-probe` run the other modules.
		if err != nil {
			log.Errorf("new module `%s` error: %s", factory.Name, err)
			continue
		}

		if err = module.Register(httpMux); err != nil {
			log.Errorf("error registering HTTP endpoints for module `%s` error: %s", factory.Name, err)
			continue
		}

		l.modules[factory.Name] = module

		log.Infof("module: %s started", factory.Name)
	}

	l.httpMux = httpMux
	l.cfg = cfg
	if len(l.modules) == 0 {
		return errors.New("no module could be loaded")
	}

	return nil
}

// GetStats returns the stats from all modules, namespaced by their names
func GetStats() map[config.ModuleName]interface{} {
	l.Lock()
	defer l.Unlock()

	stats := make(map[config.ModuleName]interface{})
	for name, module := range l.modules {
		stats[name] = module.GetStats()
	}
	return stats
}

// RestartModule triggers a module restart
func RestartModule(factory Factory) error {
	l.Lock()
	defer l.Unlock()

	if l.closed == true {
		return fmt.Errorf("can't restart module because system-probe is shutting down")
	}

	currentModule := l.modules[factory.Name]
	if currentModule == nil {
		return fmt.Errorf("module %s is not running", factory.Name)
	}
	currentModule.Close()

	newModule, err := factory.Fn(l.cfg)
	if err != nil {
		return err
	}
	log.Infof("module %s restarted", factory.Name)

	err = newModule.Register(l.httpMux)
	if err != nil {
		return err
	}

	l.modules[factory.Name] = newModule
	return nil
}

// Close each registered module
func Close() {
	l.Lock()
	defer l.Unlock()

	if l.closed == true {
		return
	}

	l.closed = true
	for _, module := range l.modules {
		module.Close()
	}
}
