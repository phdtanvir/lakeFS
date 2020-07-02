package auth

import (
	"context"
	"math/rand"
	"runtime"
	"time"

	"github.com/google/uuid"
	"github.com/treeverse/lakefs/config"
	"github.com/treeverse/lakefs/db"
	"github.com/treeverse/lakefs/logging"
)

func UpdateMetadataValues(authService Service) (map[string]string, error) {
	metadata := make(map[string]string)
	metadata["lakefs_version"] = config.Version
	metadata["golang_version"] = runtime.Version()
	metadata["architecture"] = runtime.GOARCH
	metadata["os"] = runtime.GOOS

	// read all the DB values, if applicable
	type HasDatabase interface {
		DB() db.Database
	}
	if d, ok := authService.(HasDatabase); ok {
		conn := d.DB()
		dbMeta, err := conn.Metadata()
		if err == nil {
			for k, v := range dbMeta {
				metadata[k] = v
			}
		}
	}

	// write everything.
	for k, v := range metadata {
		err := authService.SetAccountMetadataKey(k, v)
		if err != nil {
			return nil, err
		}
	}

	return metadata, nil

}

func WriteInitialMetadata(authService Service) (string, map[string]string, error) {

	err := authService.SetAccountMetadataKey("setup_time", time.Now().Format(time.RFC3339))
	if err != nil {
		return "", nil, err
	}

	installationID := uuid.Must(uuid.NewUUID()).String()
	err = authService.SetAccountMetadataKey("installation_id", installationID)
	if err != nil {
		return "", nil, err
	}

	meta, err := UpdateMetadataValues(authService)
	if err != nil {
		return "", nil, err
	}

	return installationID, meta, nil
}

type MetadataRefresher struct {
	splay       time.Duration
	interval    time.Duration
	authService Service
	stop        chan bool
	done        chan bool
}

func NewMetadataRefresher(splay, interval time.Duration, authService Service) *MetadataRefresher {
	return &MetadataRefresher{
		splay:       splay,
		interval:    interval,
		authService: authService,
		stop:        make(chan bool),
		done:        make(chan bool),
	}
}

func (m *MetadataRefresher) Start() {
	go func() {
		// sleep random 0-splay
		r := rand.New(rand.NewSource(time.Now().UnixNano()))
		splayRandTime := rand.Intn(r.Intn(int(m.splay)))
		splayRandDuration := time.Duration(splayRandTime)
		log := logging.Default().WithFields(logging.Fields{
			"splay":    splayRandDuration.String(),
			"interval": m.interval,
		})
		log.Trace("starting metadata refresher")
		stillRunning := true

		select {
		case <-m.stop:
			stillRunning = false
		case <-time.After(splayRandDuration):
			m.update()
		}

		for stillRunning {
			select {
			case <-m.stop:
				stillRunning = false
				break
			case <-time.After(m.interval):
				m.update()
			}
		}
		m.done <- true
	}()
}

func (m *MetadataRefresher) update() {
	_, err := UpdateMetadataValues(m.authService)
	if err != nil {
		logging.Default().WithError(err).Debug("failed refreshing local metadata values")
		return
	}
	_, err = m.authService.GetAccountMetadataKey("installation_id")
	if err != nil {
		logging.Default().WithError(err).Debug("failed fetching installation ID")
		return
	}
	logging.Default().Trace("local metadata refreshed")
}

func (m *MetadataRefresher) Shutdown(ctx context.Context) error {
	go func() { m.stop <- true }()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-m.done:
		return nil
	}
}