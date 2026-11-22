// Copyright 2018-2024 CERN
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
// In applying this license, CERN does not waive the privileges and immunities
// granted to it by virtue of its status as an Intergovernmental Organization
// or submit itself to any jurisdiction.

package options

import (
	"path/filepath"
	"time"

	"github.com/go-viper/mapstructure/v2"
	decomposedoptions "github.com/opencloud-eu/reva/v3/pkg/storage/pkg/decomposedfs/options"
	"github.com/pkg/errors"
)

type Options struct {
	decomposedoptions.Options

	// acting as the space's group; implied by use_space_owner, which also acts as its uid
	UseSpaceGroups bool `mapstructure:"use_space_groups"`

	ScanDebounceDelay time.Duration `mapstructure:"scan_debounce_delay"`

	// Allows generating revisions from changes done to the local storage.
	// Note: This basically doubles the number of bytes stored on disk because
	// a copy of the current version of a file is kept available for generating
	// a revision when the file is changed.
	EnableFSRevisions bool `mapstructure:"enable_fs_revisions"`

	// glob patterns for directory names (at any depth) that are neither scanned, watched nor listed, e.g. ".*" or "@Recycle"
	IgnoredDirPatterns []string `mapstructure:"ignored_dir_patterns"`

	ScanFS                   bool   `mapstructure:"scan_fs"`
	WatchFS                  bool   `mapstructure:"watch_fs"`
	WatchType                string `mapstructure:"watch_type"`
	WatchPath                string `mapstructure:"watch_path"`
	WatchRoot                string `mapstructure:"watch_root"` // base directory for the watch. events will be considered relative to this path
	WatchNotificationBrokers string `mapstructure:"watch_notification_brokers"`

	NatsWatcher NatsWatcherConfig `mapstructure:"natswatcher"`

	SMBNotify SMBNotifyConfig `mapstructure:"smbnotify"`

	// InotifyWatcher specific options
	InotifyStatsFrequency time.Duration `mapstructure:"inotify_stats_frequency"`
}

// SMBNotifyConfig connects the smbnotify watcher to the SMB server that serves the same tree as the root
type SMBNotifyConfig struct {
	// host:port of the SMB server, e.g. nas.example.com:445
	Address string `mapstructure:"address"`
	// the share and the directory inside it that correspond to watch_root (empty = the share root)
	Share string `mapstructure:"share"`
	Path  string `mapstructure:"path"`
	// the account the watcher authenticates as; it only ever reads directory change notifications
	Username string `mapstructure:"username"`
	Password string `mapstructure:"password"`
	Domain   string `mapstructure:"domain"`
	// wait between reconnect attempts; 0 uses 5s
	ReconnectDelay time.Duration `mapstructure:"reconnect_delay"`
}

// NatsWatcherConfig is the configuration needed for a NATS watcher event stream.
type NatsWatcherConfig struct {
	Endpoint             string        `mapstructure:"address"`
	Cluster              string        `mapstructure:"clusterID"`
	Stream               string        `mapstructure:"stream"`
	Durable              string        `mapstructure:"durable-name"`
	TLSInsecure          bool          `mapstructure:"tls-insecure"`
	TLSRootCACertificate string        `mapstructure:"tls-root-ca-cert"`
	EnableTLS            bool          `mapstructure:"enable-tls"`
	AuthUsername         string        `mapstructure:"username"`
	AuthPassword         string        `mapstructure:"password"`
	MaxAckPending        int           `mapstructure:"max-ack-pending"`
	AckWait              time.Duration `mapstructure:"ack-wait"`
}

// New returns a new Options instance for the given configuration
func New(m map[string]interface{}) (*Options, error) {
	// default to hybrid metadatabackend for posixfs
	if _, ok := m["metadata_backend"]; !ok {
		m["metadata_backend"] = "hybrid"
	}
	if _, ok := m["scan_debounce_delay"]; !ok {
		m["scan_debounce_delay"] = 10 * time.Millisecond
	}
	if _, ok := m["inotify_stats_frequency"]; !ok {
		m["inotify_stats_frequency"] = 5 * time.Minute
	}

	o := &Options{}
	if err := mapstructure.Decode(m, o); err != nil {
		err = errors.Wrap(err, "error decoding conf")
		return nil, err
	}

	do, err := decomposedoptions.New(m)
	if err != nil {
		return nil, err
	}
	o.Options = *do

	for _, pattern := range o.IgnoredDirPatterns {
		if _, err := filepath.Match(pattern, ""); err != nil {
			return nil, errors.Wrapf(err, "invalid ignored_dir_patterns entry %q", pattern)
		}
	}

	// acting as the owner includes acting as the owner's group
	if o.UseSpaceOwner {
		o.UseSpaceGroups = true
	}

	return o, nil
}
