package cmd

import (
	"context"
	"errors"

	"github.com/zk-org/zk/internal/adapter/s3"
	"github.com/zk-org/zk/internal/cli"
	"github.com/zk-org/zk/internal/core"
)

// newS3Backend builds the S3 synchronization backend for a notebook.
//
// The configuration is checked before anything is opened, so that an
// incomplete one is reported as the setting it is missing rather than as a
// failed request — the same courtesy the Git backend does a missing remote.
func newS3Backend(container *cli.Container, notebook *core.Notebook) (core.SyncBackend, error) {
	config := notebook.Config.Sync.S3

	if config.Bucket.IsNull() {
		return nil, errors.New("no S3 bucket to synchronize with: set `bucket` under `[sync.s3]` in the notebook's configuration")
	}

	client, err := s3.NewClient(context.Background(), config)
	if err != nil {
		return nil, err
	}

	return s3.NewBackend(s3.BackendOpts{
		NotebookPath: notebook.Path,
		Store:        client,
		Bucket:       config.Bucket.Unwrap(),
		Prefix:       config.Prefix.Unwrap(),
		Exclude:      notebook.Config.Sync.Exclude,
		MaxDeletes:   config.MaxDeletes,
		Stdout:       container.Stdout,
		Stderr:       container.Stderr,
	}), nil
}
