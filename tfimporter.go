package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/PlakarKorp/kloset/connectors"
	"github.com/PlakarKorp/kloset/location"
	"github.com/PlakarKorp/kloset/objects"
)

type TfImporter struct {
	server   string
	username string
	name     string
	state    []byte
}

func NewTfImporter(server string, username string, name string, state []byte) TfImporter {
	return TfImporter{
		server:   server,
		username: username,
		name:     name,
		state:    state,
	}
}

func (m TfImporter) Import(ctx context.Context, record chan<- *connectors.Record, result <-chan *connectors.Result) error {
	defer close(record)
	fi := objects.FileInfo{
		Lname:    m.name,
		Lsize:    int64(len(m.state)),
		Lmode:    0o644,
		LmodTime: time.Now(),
		Ldev:     1,
	}
	entry := connectors.NewRecord(fmt.Sprintf("/%s", m.name), "", fi, nil, func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(m.state)), nil })
	select {
	case record <- entry:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case _, ok := <-result:
		if !ok {
			return errors.New("ack failed")
		}
	case <-ctx.Done():
		return ctx.Err()
	}
	return nil
}

func (m TfImporter) Ping(ctx context.Context) error {
	return nil
}

func (m TfImporter) Close(ctx context.Context) error {
	return nil
}

func (m TfImporter) Flags() location.Flags {
	return location.FLAG_FILE | location.FLAG_NEEDACK
}

func (m TfImporter) Type() string {
	return "tfstate-server"
}

func (m TfImporter) Origin() string {
	return fmt.Sprintf("%s@%s", m.username, m.server)
}

func (m TfImporter) Root() string {
	return "/"
}
