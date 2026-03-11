package configwatch

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/fsnotify/fsnotify"
	"github.com/uptrace/opentelemetry-go-extra/otelzap"
	"go.uber.org/zap"
)

// Watcher watches a directory for file changes and maintains a cache
type Watcher struct {
	dir       string
	fswatcher *fsnotify.Watcher
	Cache     *sync.Map
	logger    *otelzap.Logger
	callbacks []func(key, value string)
}

// NewWatcher creates a directory watcher that updates the cache when files change
func NewWatcher(dir string, logger *otelzap.Logger) (*Watcher, error) {
	if len(dir) < 1 {
		return nil, errors.New("directory is empty")
	}

	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	w := &Watcher{
		dir:       dir,
		fswatcher: fw,
		Cache:     new(sync.Map),
		logger:    logger,
		callbacks: make([]func(key, value string), 0),
	}

	w.logger.Info("config watcher starting", zap.String("dir", w.dir))
	err = w.fswatcher.Add(w.dir)
	if err != nil {
		return nil, err
	}

	// Initial read
	if err := w.updateCache(); err != nil {
		return nil, err
	}

	return w, nil
}

// OnChange registers a callback to be called when a config file changes
func (w *Watcher) OnChange(callback func(key, value string)) {
	w.callbacks = append(w.callbacks, callback)
}

// Watch starts watching for file changes in a goroutine
// Kubernetes kubelet updates ConfigMap/Secret mounts by creating a new ..data symlink
func (w *Watcher) Watch() {
	go func() {
		for {
			select {
			case event := <-w.fswatcher.Events:
				// Kubernetes updates ConfigMaps/Secrets by recreating the ..data symlink
				if event.Op&fsnotify.Create == fsnotify.Create {
					if filepath.Base(event.Name) == "..data" {
						if err := w.updateCache(); err != nil {
							w.logger.Error("config watcher update failed", zap.Error(err))
						} else {
							w.logger.Info("config watcher reloaded", zap.String("dir", w.dir))
						}
					}
				}
			case err := <-w.fswatcher.Errors:
				w.logger.Error("config watcher error", zap.String("dir", w.dir), zap.Error(err))
			}
		}
	}()
}

// Get retrieves a value from the cache
func (w *Watcher) Get(key string) (string, bool) {
	if v, ok := w.Cache.Load(key); ok {
		return v.(string), true
	}
	return "", false
}

// GetAll returns all cached config values
func (w *Watcher) GetAll() map[string]string {
	result := make(map[string]string)
	w.Cache.Range(func(key, value interface{}) bool {
		result[key.(string)] = value.(string)
		return true
	})
	return result
}

// Close stops the watcher
func (w *Watcher) Close() error {
	return w.fswatcher.Close()
}

// updateCache reads files and updates the cache
func (w *Watcher) updateCache() error {
	fileMap := make(map[string]string)
	files, err := os.ReadDir(w.dir)
	if err != nil {
		return err
	}

	// Read files, ignoring symlinks and subdirectories
	for _, file := range files {
		name := filepath.Base(file.Name())
		// Skip hidden files and Kubernetes symlinks (..data, ..xxx_tmp)
		if !file.IsDir() && !strings.HasPrefix(name, "..") {
			b, err := os.ReadFile(filepath.Join(w.dir, file.Name()))
			if err != nil {
				return err
			}
			fileMap[name] = strings.TrimSpace(string(b))
		}
	}

	// Remove deleted files from cache
	w.Cache.Range(func(key, value interface{}) bool {
		if _, ok := fileMap[key.(string)]; !ok {
			w.Cache.Delete(key)
		}
		return true
	})

	// Update cache and trigger callbacks for changed values
	for k, v := range fileMap {
		oldValue, existed := w.Cache.Load(k)
		w.Cache.Store(k, v)

		// Trigger callbacks if value changed
		if !existed || oldValue.(string) != v {
			for _, cb := range w.callbacks {
				cb(k, v)
			}
		}
	}

	return nil
}
