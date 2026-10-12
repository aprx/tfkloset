package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/PlakarKorp/integrations/fs/storage"
	"github.com/PlakarKorp/kloset/caching"
	"github.com/PlakarKorp/kloset/caching/pebble"
	"github.com/PlakarKorp/kloset/connectors/storage"
	"github.com/PlakarKorp/kloset/encryption"
	"github.com/PlakarKorp/kloset/kcontext"
	"github.com/PlakarKorp/kloset/logging"
	"github.com/PlakarKorp/kloset/objects"
	"github.com/PlakarKorp/kloset/repository"
	"github.com/PlakarKorp/kloset/snapshot"
	"go.yaml.in/yaml/v2"
)

type Config struct {
	Name   string `yaml:"name"`
	Server struct {
		Address string `yaml:"address"`
		Port    int    `yaml:"port"`
	} `yaml:"server"`
	Store struct {
		Path string `yaml:"path"`
	} `yaml:"store"`
}

type BackendStore struct {
	store  storage.Store
	config []byte
}

type TfKloset struct {
	Kctx         *kcontext.KContext
	StorePath    string
	cacheManager *caching.Manager
	BStore       *BackendStore
	server       *http.Server
	config       *Config
}

func (t *TfKloset) Close() error {
	_ = t.cacheManager.Close()
	return nil
}

func newTfKloset(config *Config) *TfKloset {
	kctx := kcontext.NewKContext()
	kctx.MaxConcurrency = 4
	logger := logging.NewLogger(os.Stdout, os.Stderr)
	logger.EnableInfo()
	kctx.SetLogger(logger)

	cache := caching.NewManager(pebble.InMemoryConstructor())
	kctx.SetCache(cache)

	slog.Info("Using storage", "path", config.Store.Path)
	return &TfKloset{
		Kctx:         kctx,
		StorePath:    config.Store.Path,
		cacheManager: cache,
		config:       config,
	}
}

func (t *TfKloset) OpenRepository(secret string) (*repository.Repository, error) {
	config, err := storage.NewConfigurationFromWrappedBytes(t.BStore.config)
	if err != nil {
		return nil, err
	}
	key, err := encryption.DeriveKey(config.Encryption.KDFParams, []byte(secret))
	if err != nil {
		return nil, err
	}
	if !encryption.VerifyCanary(config.Encryption, key) {
		return nil, errors.New("cannot verify canary")
	}
	repo, err := repository.NewRepository(t.Kctx, key, t.BStore.store, t.BStore.config, &repository.RepositoryOpts{DoRebuild: true, RWStateCache: true})
	if err != nil {
		return nil, err
	}
	return repo, nil
}

func (t *TfKloset) OpenStore() error {
	config := map[string]string{}
	config["location"] = fmt.Sprintf("fs://%s", t.StorePath)

	store, serializedConfig, err := storage.Open(t.Kctx, config)
	if err != nil {
		return err
	}
	backendStore := &BackendStore{
		store: store,
	}
	backendStore.config = serializedConfig[:]
	t.BStore = backendStore
	return nil
}

func (t *TfKloset) GetLatestSnapshotWithName(repo *repository.Repository, name string) (*snapshot.Snapshot, error) {
	var snap *snapshot.Snapshot
	for id, err := range repo.ListSnapshots() {
		if err != nil {
			if snap != nil {
				_ = snap.Close()
			}
			return nil, err
		}
		cur, err := snapshot.Load(repo, id)
		if err != nil {
			if snap != nil {
				_ = snap.Close()
			}
			return nil, err
		}
		if cur.Header.Name != name {
			_ = cur.Close()
			continue
		}
		if snap == nil {
			snap = cur
			continue
		}
		if cur.Header.Timestamp.After(snap.Header.Timestamp) {
			_ = snap.Close()
			snap = cur
			continue
		}
		_ = cur.Close()
	}
	return snap, nil
}

func (t *TfKloset) GetState(w http.ResponseWriter, r *http.Request) {
	stateName := r.PathValue("name")

	userAuthKey, ok := UserAuthKeyFromContext(r.Context())
	if !ok {
		slog.Error("No userAuthKey")
		http.Error(w, "How did you get there ?", http.StatusInternalServerError)
		return
	}

	repo, err := t.OpenRepository(userAuthKey.Passphrase)
	if err != nil {
		slog.Warn("Cannot open repository", "name", stateName, "err", err)
		http.Error(w, "repository error", http.StatusInternalServerError)
		return
	}
	defer func() { _ = repo.Close() }()
	snap, err := t.GetLatestSnapshotWithName(repo, stateName)
	if err != nil {
		slog.Warn("Cannot fetch latest snapshot", "name", stateName, "err", err)
		http.Error(w, "snapshot error ", http.StatusInternalServerError)
		return
	}
	if snap == nil {
		slog.Warn("no snapshots in the kloset", "name", stateName, "err", err)
		http.Error(w, "state not found", http.StatusNotFound)
		return
	}
	defer func() { _ = snap.Close() }()
	reader, err := snap.NewReader("/state.json")
	if err != nil {
		slog.Warn("no state found in the snapshot", "name", stateName, "err", err)
		http.Error(w, "no state found", http.StatusNotFound)
		return
	}
	defer func() { _ = reader.Close() }()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, reader)
}

func (t *TfKloset) PostState(w http.ResponseWriter, r *http.Request) {
	stateName := r.PathValue("name")

	userAuthKey, ok := UserAuthKeyFromContext(r.Context())
	if !ok {
		slog.Error("No userAuthKey")
		http.Error(w, "How did you get there ?", http.StatusInternalServerError)
		return
	}

	repo, err := t.OpenRepository(userAuthKey.Passphrase)
	if err != nil {
		slog.Warn("Cannot open repository", "name", stateName, "err", err)
		http.Error(w, "cannot open repository", http.StatusInternalServerError)
		return
	}
	defer func() { _ = repo.Close() }()
	data, err := io.ReadAll(r.Body)
	if err != nil {
		slog.Warn("Cannot read body", "err", err)
		http.Error(w, "cannot read body", http.StatusBadRequest)
		return
	}
	myImp := NewTfImporter(t.config.Name, userAuthKey.Username, "state.json", data)
	source, err := snapshot.NewSource(r.Context(), myImp)
	if err != nil {
		slog.Warn("Cannot initiate source", "err", err)
		http.Error(w, "cannot initiate source", http.StatusInternalServerError)
		return
	}
	builder, err := snapshot.Create(repo, repository.DefaultType, os.TempDir(), objects.NilMac, &snapshot.BuilderOptions{Name: stateName})
	if err != nil {
		slog.Warn("Cannot create snapshot", "err", err)
		http.Error(w, "cannot create snapshot", http.StatusInternalServerError)
		return
	}
	defer func() { _ = builder.Close() }()

	if err := builder.Import(source); err != nil {
		slog.Warn("Cannot import state", "err", err)
		http.Error(w, "cannot import state", http.StatusInternalServerError)
		return
	}
	if err := builder.Commit(); err != nil {
		slog.Warn("Cannot commit snapshot", "err", err)
		http.Error(w, "cannot commit snapshot", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func loadConfig(path string) (*Config, error) {
	var config Config
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, err
	}

	return &config, nil
}

func (t *TfKloset) setupServer() {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{name}", t.GetState)
	mux.HandleFunc("POST /{name}", t.PostState)
	t.server = &http.Server{
		Addr:    fmt.Sprintf("%s:%d", t.config.Server.Address, t.config.Server.Port),
		Handler: LoggingMiddleware(AuthMiddleware(mux)),
	}
}

func (t *TfKloset) Start() error {
	if err := t.OpenStore(); err != nil {
		slog.Error("Cannot open store", "err", err)
		return err
	}
	t.setupServer()

	ctx, stop := signal.NotifyContext(context.Background(),
		os.Interrupt,
		syscall.SIGTERM)
	defer stop()

	go func() {
		if err := t.listenAndServe(); err != nil {
			if !errors.Is(err, http.ErrServerClosed) {
				slog.Error("http server failed", "err", err)
				stop()
			}
		}
	}()
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := t.server.Shutdown(shutdownCtx); err != nil {
		slog.Error("http server shutdown", "err", err)
		return err
	}

	return nil
}

func (t *TfKloset) listenAndServe() error {
	slog.Info("Starting server")
	return t.server.ListenAndServe()
}

func main() {
	configPath := "./tfkloset.yaml"

	if len(os.Args) > 1 {
		configPath = os.Args[1]
	}
	slog.Info("Using configuration file", "path", configPath)
	config, err := loadConfig(configPath)
	if err != nil {
		panic(err)
	}
	tKloset := newTfKloset(config)

	err = tKloset.Start()
	slog.Info("good bye", "err", err)
	_ = tKloset.Close()
}
