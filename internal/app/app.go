package app

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/langrenjh-alt/S2AM-GO/internal/auditlog"
	"github.com/langrenjh-alt/S2AM-GO/internal/config"
	"github.com/langrenjh-alt/S2AM-GO/internal/secret"
	"github.com/langrenjh-alt/S2AM-GO/internal/upstream"
)

type App struct {
	config               config.Config
	db                   *pgxpool.Pool
	cipher               *secret.Cipher
	httpClient           *http.Client
	versions             *versionChecker
	auditLog             *auditlog.Store
	logger               *slog.Logger
	balanceRefreshSignal chan struct{}
}

func New(cfg config.Config, db *pgxpool.Pool, logger *slog.Logger) (*App, error) {
	cipher, err := secret.New(cfg.MasterKey)
	if err != nil {
		return nil, err
	}
	auditStore, err := auditlog.New(cfg.LogDir)
	if err != nil {
		return nil, err
	}
	httpClient := upstream.NewHTTPClient(cfg.AllowPrivateUpstreams)
	application := &App{
		config:               cfg,
		db:                   db,
		cipher:               cipher,
		httpClient:           httpClient,
		versions:             newVersionChecker(httpClient),
		auditLog:             auditStore,
		logger:               logger,
		balanceRefreshSignal: make(chan struct{}, 1),
	}
	if err := application.migrateLegacyAuditEvents(context.Background()); err != nil {
		return nil, err
	}
	logger.Info("audit log ready", slog.String("directory", auditStore.Directory()))
	return application, nil
}

func (a *App) Ready(ctx context.Context) error {
	return a.db.Ping(ctx)
}
